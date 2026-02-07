package worker

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"lifeops/internal/config"
	"lifeops/internal/llm"
	"lifeops/internal/store"
	"lifeops/internal/telegram"
)

const maxToolIterations = 10

type SummaryStore interface {
	RecentFinanceSummary(ctx context.Context, since time.Time) (string, error)
	RecentWorkouts(ctx context.Context, since time.Time, limit int) ([]store.Workout, error)
	RecentSleep(ctx context.Context, since time.Time, limit int) ([]store.Sleep, error)
	RecentMetrics(ctx context.Context, since time.Time, limit int, kinds []string) ([]store.Metric, error)
	RecentNutritionEntries(ctx context.Context, chatID int64, since time.Time, limit int) ([]store.NutritionEntry, error)
	CalendarEventsBetween(ctx context.Context, calendarID string, start, end time.Time, limit int) ([]store.CalendarEvent, error)
	ChatHistory(ctx context.Context, chatID int64, agent string, limit int) ([]store.ChatMessage, error)
	SaveChatMessage(ctx context.Context, chatID int64, agent string, role string, content string) error
	ChatIDsForAgent(ctx context.Context, agent string) ([]int64, error)
	GetUserMemories(ctx context.Context, agent string) ([]store.UserMemory, error)
}

type TriggerType string

const (
	TriggerScheduled     TriggerType = "scheduled"
	TriggerDataIngestion TriggerType = "data_ingestion"
	TriggerManual        TriggerType = "manual"
)

type DebugInfo struct {
	TriggerType  TriggerType
	TriggerTime  time.Time
	ToolCalls    []ToolCallDebug
	Enabled      bool
}

type ToolCallDebug struct {
	Name      string
	Arguments string
	Result    string
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
	reviewTimes    []config.DailyReviewTime
	debugInfo      *DebugInfo
}

func New(store SummaryStore, provider llm.Provider, sender telegram.Sender, agent config.AgentConfig, historyLimit int, defaultChatIDs []int64) *Worker {
	if agent.DailyReviewPrompt == "" {
		agent.DailyReviewPrompt = config.DefaultDailyReviewPrompt
	}
	if agent.Timezone == nil {
		agent.Timezone = time.UTC
	}
	if strings.EqualFold(agent.Name, "nutrition") && agent.NutritionReviewChatID != 0 {
		defaultChatIDs = []int64{agent.NutritionReviewChatID}
	}
	reviewTimes := agent.DailyReviewTimes
	if len(reviewTimes) == 0 {
		if agent.DailyReviewTime.Hour == 0 && agent.DailyReviewTime.Minute == 0 {
			agent.DailyReviewTime = config.DailyReviewTime{Hour: 9, Minute: 0}
		}
		reviewTimes = []config.DailyReviewTime{agent.DailyReviewTime}
	}
	reviewTimes = normalizeReviewTimes(reviewTimes)
	agent.DailyReviewTime = reviewTimes[0]
	agent.DailyReviewTimes = reviewTimes
	log.Printf("worker[%s] initialized with defaultChatIDs=%v", agent.Name, defaultChatIDs)
	return &Worker{
		store:          store,
		provider:       provider,
		sender:         sender,
		agent:          agent,
		historyLimit:   historyLimit,
		defaultChatIDs: defaultChatIDs,
		tools:          newToolRegistry(store, agent.Timezone, logChatIDs(agent.NutritionLogChatID)),
		reviewTimes:    reviewTimes,
	}
}

func (w *Worker) SetObserver(observer WorkerObserver) {
	w.observer = observer
}

func (w *Worker) EnableDebug(triggerType TriggerType) {
	w.debugInfo = &DebugInfo{
		TriggerType: triggerType,
		TriggerTime: time.Now(),
		ToolCalls:   []ToolCallDebug{},
		Enabled:     true,
	}
}

func (w *Worker) RunDaily(ctx context.Context) error {
	if len(w.reviewTimes) == 0 {
		return fmt.Errorf("no review times configured")
	}
	next := w.nextReviewTime(time.Now(), true)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Until(next)):
			// Enable debug mode for scheduled reviews
			w.EnableDebug(TriggerScheduled)
			if err := w.RunOnce(ctx); err != nil {
				return err
			}
			next = w.nextReviewTime(next, false)
		}
	}
}

func (w *Worker) nextReviewTime(after time.Time, inclusive bool) time.Time {
	tz := w.agent.Timezone
	if tz == nil {
		tz = time.UTC
	}
	local := after.In(tz)
	for _, slot := range w.reviewTimes {
		candidate := time.Date(local.Year(), local.Month(), local.Day(), slot.Hour, slot.Minute, 0, 0, tz)
		if candidate.Before(local) {
			continue
		}
		if !inclusive && candidate.Equal(local) {
			continue
		}
		return candidate
	}
	nextDay := local.AddDate(0, 0, 1)
	first := w.reviewTimes[0]
	return time.Date(nextDay.Year(), nextDay.Month(), nextDay.Day(), first.Hour, first.Minute, 0, 0, tz)
}

func (w *Worker) RunOnce(ctx context.Context) error {
	dbChatIDs, err := w.store.ChatIDsForAgent(ctx, w.agent.Name)
	if err != nil {
		return err
	}
	chatIDs := w.resolveChatIDs(dbChatIDs)
	if len(chatIDs) == 0 {
		log.Printf("worker[%s] no chat IDs resolved (db returned %d, defaults=%v)",
			w.agent.Name, len(dbChatIDs), w.defaultChatIDs)
		return nil
	}
	log.Printf("worker[%s] running for %d chat(s): %v", w.agent.Name, len(chatIDs), chatIDs)
	for _, chatID := range chatIDs {
		if err := w.runDailyReview(ctx, chatID); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) resolveChatIDs(ids []int64) []int64 {
	filtered := w.filterReviewChats(ids)
	if len(filtered) == 0 {
		// Use configured default chat IDs when no chat history exists
		// This handles the "cold start" case for new agents
		filtered = append(filtered, w.defaultChatIDs...)
	}
	result := dedupInt64(filtered)
	if len(result) == 0 && len(ids) > 0 {
		// Defensive fallback: if we had chat IDs from DB but filtering removed them all,
		// use the original list (this can happen with nutrition agent filtering)
		log.Printf("worker[%s] warning: all chat IDs filtered out, using original list", w.agent.Name)
		return dedupInt64(ids)
	}
	return result
}

func (w *Worker) filterReviewChats(ids []int64) []int64 {
	if !strings.EqualFold(w.agent.Name, "nutrition") || w.agent.NutritionReviewChatID == 0 {
		return ids
	}
	var filtered []int64
	for _, id := range ids {
		if id == w.agent.NutritionReviewChatID {
			filtered = append(filtered, id)
		}
	}
	return filtered
}

// RunForChat executes a daily review only for a specific chat without looking up chat IDs in the store.
func (w *Worker) RunForChat(ctx context.Context, chatID int64) error {
	if chatID == 0 {
		return fmt.Errorf("chat id is required")
	}
	return w.runDailyReview(ctx, chatID)
}

func (w *Worker) runDailyReview(ctx context.Context, chatID int64) error {
	log.Printf("worker[%s] starting daily review for chat %d", w.agent.Name, chatID)
	w.tools.SetChatContext(chatID)
	history, err := w.store.ChatHistory(ctx, chatID, w.agent.Name, w.historyLimit)
	if err != nil {
		return fmt.Errorf("load chat history: %w", err)
	}
	log.Printf("worker[%s] loaded %d history messages for chat %d", w.agent.Name, len(history), chatID)
	systemPrompt := telegram.ResolvePrompt(w.agent.Name, w.agent.Prompt)
	// Load and inject user memories into system prompt
	memories, err := w.store.GetUserMemories(ctx, w.agent.Name)
	if err != nil {
		log.Printf("failed to load memories: %v", err)
	} else if len(memories) > 0 {
		systemPrompt = injectMemoriesIntoPrompt(systemPrompt, memories)
	}
	dailyPrompt := w.buildDailyPrompt()
	messages := w.composeMessages(systemPrompt, history, dailyPrompt)
	if err := w.store.SaveChatMessage(ctx, chatID, w.agent.Name, "user", dailyPrompt); err != nil {
		return fmt.Errorf("save user message: %w", err)
	}
	response, err := w.completeWithTools(ctx, messages)
	if err != nil {
		return fmt.Errorf("generate response: %w", err)
	}

	// Append debug information if enabled
	debugSuffix := w.formatDebugInfo()
	messageToSend := response + debugSuffix

	if err := w.store.SaveChatMessage(ctx, chatID, w.agent.Name, "assistant", response); err != nil {
		return fmt.Errorf("save assistant message: %w", err)
	}
	log.Printf("worker[%s] sending message to chat %d (length: %d chars, debug: %v)", w.agent.Name, chatID, len(messageToSend), w.debugInfo != nil && w.debugInfo.Enabled)
	if err := w.sender.SendMessage(ctx, chatID, messageToSend); err != nil {
		return fmt.Errorf("send telegram message: %w", err)
	}
	log.Printf("worker[%s] successfully completed daily review for chat %d", w.agent.Name, chatID)
	return nil
}

func injectMemoriesIntoPrompt(systemPrompt string, memories []store.UserMemory) string {
	if len(memories) == 0 {
		return systemPrompt
	}
	var builder strings.Builder
	builder.WriteString(systemPrompt)
	builder.WriteString("\n\n")
	builder.WriteString("Important facts about the user:\n")
	for _, mem := range memories {
		builder.WriteString("- ")
		builder.WriteString(mem.Content)
		builder.WriteString("\n")
	}
	return builder.String()
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
	now := time.Now().In(w.agent.Timezone)
	currentTime := fmt.Sprintf("Текущее время: %s", now.Format("2006-01-02 15:04 MST"))

	base := renderDailyReviewPrompt(w.agent.DailyReviewPrompt)
	tools := w.tools.Describe()
	if tools == "" {
		return strings.TrimSpace(fmt.Sprintf("%s\n\n%s", currentTime, base))
	}
	return strings.TrimSpace(fmt.Sprintf("%s\n\n%s\n\nДоступные инструменты: %s. Вызывай их перед ответом, если нужны свежие данные. После получения данных сделай короткий вывод и рекомендации на русском языке.", currentTime, base, tools))
}

func dedupInt64(values []int64) []int64 {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(values))
	var out []int64
	for _, v := range values {
		if v == 0 {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func logChatIDs(id int64) []int64 {
	if id == 0 {
		return nil
	}
	return []int64{id}
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

			// Collect debug information
			if w.debugInfo != nil && w.debugInfo.Enabled {
				w.debugInfo.ToolCalls = append(w.debugInfo.ToolCalls, ToolCallDebug{
					Name:      call.Name,
					Arguments: strings.TrimSpace(string(call.Arguments)),
					Result:    truncateString(output, 500),
				})
			}

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

func normalizeReviewTimes(times []config.DailyReviewTime) []config.DailyReviewTime {
	if len(times) == 0 {
		return []config.DailyReviewTime{{Hour: 9, Minute: 0}}
	}
	out := make([]config.DailyReviewTime, 0, len(times))
	seen := map[string]struct{}{}
	for _, slot := range times {
		hour := slot.Hour
		minute := slot.Minute
		if hour < 0 || hour > 23 {
			continue
		}
		if minute < 0 || minute > 59 {
			continue
		}
		key := fmt.Sprintf("%02d:%02d", hour, minute)
		if _, ok := seen[key]; ok {
			continue
		}
		out = append(out, config.DailyReviewTime{Hour: hour, Minute: minute})
		seen[key] = struct{}{}
	}
	if len(out) == 0 {
		out = []config.DailyReviewTime{{Hour: 9, Minute: 0}}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hour == out[j].Hour {
			return out[i].Minute < out[j].Minute
		}
		return out[i].Hour < out[j].Hour
	})
	return out
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func (w *Worker) formatDebugInfo() string {
	if w.debugInfo == nil || !w.debugInfo.Enabled {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\n---\n")
	sb.WriteString("🔍 Debug Info:\n")

	// Trigger information
	switch w.debugInfo.TriggerType {
	case TriggerScheduled:
		sb.WriteString(fmt.Sprintf("📅 Scheduled review at %s\n", w.debugInfo.TriggerTime.Format("15:04")))
	case TriggerDataIngestion:
		sb.WriteString(fmt.Sprintf("📊 Data ingestion event at %s\n", w.debugInfo.TriggerTime.Format("15:04")))
	case TriggerManual:
		sb.WriteString(fmt.Sprintf("🔧 Manual trigger at %s\n", w.debugInfo.TriggerTime.Format("15:04")))
	}

	// Tool calls
	if len(w.debugInfo.ToolCalls) > 0 {
		sb.WriteString(fmt.Sprintf("\n🛠 Tools called (%d):\n", len(w.debugInfo.ToolCalls)))
		for i, call := range w.debugInfo.ToolCalls {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, call.Name))
			if call.Arguments != "" && call.Arguments != "{}" {
				sb.WriteString(fmt.Sprintf("   Args: %s\n", call.Arguments))
			}
		}
	}

	return sb.String()
}
