package worker

import (
	"context"
	"strings"
	"time"

	"lifeops/internal/config"
	"lifeops/internal/llm"
	"lifeops/internal/store"
	"lifeops/internal/telegram"
)

type SummaryStore interface {
	RecentHealthSummary(ctx context.Context, since time.Time) (string, error)
	MetricsSummary(ctx context.Context, since time.Time) (string, error)
	RecentFinanceSummary(ctx context.Context, since time.Time) (string, error)
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
	}
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
	since := time.Now().In(w.agent.Timezone).AddDate(0, 0, -7)

	healthSummary, err := w.store.RecentHealthSummary(ctx, since)
	if err != nil {
		return err
	}
	metricsSummary, err := w.store.MetricsSummary(ctx, since)
	if err != nil {
		return err
	}
	financeSummary, err := w.store.RecentFinanceSummary(ctx, since)
	if err != nil {
		return err
	}
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
	dailyPrompt := renderDailyReviewPrompt(w.agent.DailyReviewPrompt, healthSummary, metricsSummary, financeSummary)
	systemPrompt := telegram.ResolvePrompt(w.agent.Name, w.agent.Prompt)
	for _, chatID := range chatIDs {
		history, err := w.store.ChatHistory(ctx, chatID, w.agent.Name, w.historyLimit)
		if err != nil {
			return err
		}
		if err := w.store.SaveChatMessage(ctx, chatID, w.agent.Name, "user", dailyPrompt); err != nil {
			return err
		}
		response, err := w.provider.Chat(ctx, telegram.BuildChatPrompt(systemPrompt, dailyPrompt, history))
		if err != nil {
			return err
		}
		if err := w.store.SaveChatMessage(ctx, chatID, w.agent.Name, "assistant", response); err != nil {
			return err
		}
		if err := w.sender.SendMessage(ctx, chatID, response); err != nil {
			return err
		}
	}
	return nil
}

func renderDailyReviewPrompt(template, healthSummary, metricsSummary, financeSummary string) string {
	replacer := strings.NewReplacer(
		"{health_summary}", healthSummary,
		"{metrics_summary}", metricsSummary,
		"{finance_summary}", financeSummary,
	)
	return strings.TrimSpace(replacer.Replace(template))
}
