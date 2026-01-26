package worker

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"lifeops/internal/config"
	"lifeops/internal/llm"
	"lifeops/internal/store"
	"lifeops/internal/telegram"
)

const maxToolIterations = 5

type SummaryStore interface {
	RecentFinanceSummary(ctx context.Context, since time.Time) (string, error)
	RecentWorkouts(ctx context.Context, since time.Time, limit int) ([]store.Workout, error)
	RecentSleep(ctx context.Context, since time.Time, limit int) ([]store.Sleep, error)
	RecentMetrics(ctx context.Context, since time.Time, limit int, kinds []string) ([]store.Metric, error)
	RecentNutritionEntries(ctx context.Context, chatID int64, since time.Time, limit int) ([]store.NutritionEntry, error)
	ChatHistory(ctx context.Context, chatID int64, agent string, limit int) ([]store.ChatMessage, error)
	SaveChatMessage(ctx context.Context, chatID int64, agent string, role string, content string) error
	ChatIDsForAgent(ctx context.Context, agent string) ([]int64, error)
}

type Worker struct {
	store          SummaryStore
	provider       llm.Provider
	sender         telegram.Sender
	agent          config.AgentConfig
	historyLimit   int
	defaultChatIDs []int64
	tools          *toolRegistry
	observer       WorkerObserver
}

func New(store SummaryStore, provider llm.Provider, sender telegram.Sender, agent config.AgentConfig, historyLimit int, defaultChatIDs []int64) *Worker {
	if agent.DailyReviewPrompt == "" {
		agent.DailyReviewPrompt = config.DefaultDailyReviewPrompt
	}
	if agent.Timezone == nil {
		agent.Timezone = time.UTC
	}
	if agent.DailyReviewTime.Hour == 0 && agent.DailyReviewTime.Minute == 0 {
		agent.DailyReviewTime = config.DailyReviewTime{Hour: 9, Minute: 0}
	}
	return &Worker{
		store:          store,
		provider:       provider,
		sender:         sender,
		agent:          agent,
		historyLimit:   historyLimit,
		defaultChatIDs: defaultChatIDs,
		tools:          newToolRegistry(store, agent.Timezone),
	}
}

func (w *Worker) SetObserver(observer WorkerObserver) {
	w.observer = observer
}

func (w *Worker) RunDaily(ctx context.Context) error {
	now := time.Now().In(w.agent.Timezone)
	next := time.Date(now.Year(), now.Month(), now.Day(), w.agent.DailyReviewTime.Hour, w.agent.DailyReviewTime.Minute, 0, 0, w.agent.Timezone)
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Until(next)):
			if err := w.RunOnce(ctx); err != nil {
				return err
			}
			next = next.Add(24 * time.Hour)
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) error {
	chatIDs, err := w.store.ChatIDsForAgent(ctx, w.agent.Name)
	if err != nil {
		return err
	}
	if len(chatIDs) == 0 && len(w.defaultChatIDs) > 0 {
		chatIDs = w.defaultChatIDs
	}
	if len(chatIDs) == 0 {
		return nil
	}
	for _, chatID := range chatIDs {
		if err := w.runDailyReview(ctx, chatID); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) runDailyReview(ctx context.Context, chatID int64) error {
	w.tools.SetChatContext(chatID)
	history, err := w.store.ChatHistory(ctx, chatID, w.agent.Name, w.historyLimit)
	if err != nil {
		return err
	}
	systemPrompt := telegram.ResolvePrompt(w.agent.Name, w.agent.Prompt)
	dailyPrompt := w.buildDailyPrompt()
	messages := w.composeMessages(systemPrompt, history, dailyPrompt)
	if err := w.store.SaveChatMessage(ctx, chatID, w.agent.Name, "user", dailyPrompt); err != nil {
		return err
	}
	response, err := w.completeWithTools(ctx, messages)
	if err != nil {
		return err
	}
	if err := w.store.SaveChatMessage(ctx, chatID, w.agent.Name, "assistant", response); err != nil {
		return err
	}
	return w.sender.SendMessage(ctx, chatID, response)
}

func (w *Worker) composeMessages(systemPrompt string, history []store.ChatMessage, dailyPrompt string) []llm.ChatMessage {
	var messages []llm.ChatMessage
	if strings.TrimSpace(systemPrompt) != "" {
		messages = append(messages, llm.ChatMessage{
			Role:    "system",
			Content: systemPrompt,
		})
	}
	for i := len(history) - 1; i >= 0; i-- {
		role := strings.ToLower(strings.TrimSpace(history[i].Role))
		if role == "" {
			continue
		}
		messages = append(messages, llm.ChatMessage{
			Role:    role,
			Content: history[i].Content,
		})
	}
	messages = append(messages, llm.ChatMessage{
		Role:    "user",
		Content: dailyPrompt,
	})
	return messages
}

func (w *Worker) buildDailyPrompt() string {
	base := renderDailyReviewPrompt(w.agent.DailyReviewPrompt)
	tools := w.tools.Describe()
	if tools == "" {
		return base
	}
	return strings.TrimSpace(fmt.Sprintf("%s\n\nДоступные инструменты: %s. Вызывай их перед ответом, если нужны свежие данные. После получения данных сделай короткий вывод и рекомендации на русском языке.", base, tools))
}

func (w *Worker) completeWithTools(ctx context.Context, messages []llm.ChatMessage) (string, error) {
	req := llm.ChatRequest{
		Messages:     append([]llm.ChatMessage(nil), messages...),
		Tools:        w.tools.Definitions(),
		MaxToolCalls: 4,
	}
	for i := 0; i < maxToolIterations; i++ {
		w.notifyLLMRequest(req.Messages)
		resp, err := w.provider.Chat(ctx, req)
		if err != nil {
			return "", err
		}
		w.notifyLLMResponse(resp)
		if len(resp.ToolCalls) == 0 {
			if strings.TrimSpace(resp.Content) == "" {
				return "", fmt.Errorf("llm returned empty response")
			}
			return strings.TrimSpace(resp.Content), nil
		}
		req.Messages = append(req.Messages, llm.ChatMessage{
			Role:      "assistant",
			ToolCalls: resp.ToolCalls,
		})
		for _, call := range resp.ToolCalls {
			output, err := w.tools.Execute(ctx, call.Name, call.Arguments)
			if err != nil {
				return "", err
			}
			log.Printf("worker[%s] tool=%s args=%s", w.agent.Name, call.Name, strings.TrimSpace(string(call.Arguments)))
			w.notifyToolResult(call, output)
			req.Messages = append(req.Messages, llm.ChatMessage{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    output,
			})
		}
	}
	return "", fmt.Errorf("tool call loop exceeded %d iterations", maxToolIterations)
}

func renderDailyReviewPrompt(template string) string {
	return strings.TrimSpace(template)
}

func (w *Worker) notifyLLMRequest(messages []llm.ChatMessage) {
	if w.observer == nil {
		return
	}
	w.observer.OnLLMRequest(w.agent.Name, cloneMessages(messages))
}

func (w *Worker) notifyLLMResponse(resp llm.ChatResponse) {
	if w.observer == nil {
		return
	}
	w.observer.OnLLMResponse(w.agent.Name, cloneResponse(resp))
}

func (w *Worker) notifyToolResult(call llm.ToolCall, output string) {
	if w.observer == nil {
		return
	}
	w.observer.OnToolResult(w.agent.Name, call, output)
}

func cloneMessages(messages []llm.ChatMessage) []llm.ChatMessage {
	out := make([]llm.ChatMessage, len(messages))
	copy(out, messages)
	return out
}

func cloneResponse(resp llm.ChatResponse) llm.ChatResponse {
	out := llm.ChatResponse{
		Content: resp.Content,
	}
	if len(resp.ToolCalls) > 0 {
		out.ToolCalls = make([]llm.ToolCall, len(resp.ToolCalls))
		copy(out.ToolCalls, resp.ToolCalls)
	}
	return out
}
