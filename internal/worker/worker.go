package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"lifeops/internal/llm"
	"lifeops/internal/telegram"
)

type DailyReport struct {
	Coach   string `json:"coach"`
	Sleep   string `json:"sleep"`
	Finance string `json:"finance"`
}

type SummaryStore interface {
	RecentHealthSummary(ctx context.Context, since time.Time) (string, error)
	MetricsSummary(ctx context.Context, since time.Time) (string, error)
	RecentFinanceSummary(ctx context.Context, since time.Time) (string, error)
}

type Worker struct {
	store    SummaryStore
	provider llm.Provider
	sender   telegram.Sender
	timezone *time.Location
	chatID   int64
}

func New(store SummaryStore, provider llm.Provider, sender telegram.Sender, timezone *time.Location, chatID int64) *Worker {
	return &Worker{store: store, provider: provider, sender: sender, timezone: timezone, chatID: chatID}
}

func (w *Worker) RunDaily(ctx context.Context) error {
	now := time.Now().In(w.timezone)
	next := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, w.timezone).Add(24 * time.Hour)
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
	since := time.Now().In(w.timezone).AddDate(0, 0, -7)
	longer := time.Now().In(w.timezone).AddDate(0, 0, -30)

	healthSummary, err := w.store.RecentHealthSummary(ctx, since)
	if err != nil {
		return err
	}
	metricsSummary, err := w.store.MetricsSummary(ctx, since)
	if err != nil {
		return err
	}
	financeSummary, err := w.store.RecentFinanceSummary(ctx, longer)
	if err != nil {
		return err
	}
	prompt := fmt.Sprintf(`You are generating a daily digest in JSON.
Return a JSON object with keys coach, sleep, finance.
Health summary: %s
Metrics summary: %s
Finance summary (last 30 days): %s`, healthSummary, metricsSummary, financeSummary)
	jsonPayload, err := w.provider.GenerateJSON(ctx, prompt)
	if err != nil {
		return err
	}
	var report DailyReport
	if err := json.Unmarshal([]byte(jsonPayload), &report); err != nil {
		return fmt.Errorf("parse report json: %w", err)
	}
	if w.chatID == 0 {
		return nil
	}
	message := FormatTelegramMessage(report)
	return w.sender.SendMessage(ctx, w.chatID, message)
}

func FormatTelegramMessage(report DailyReport) string {
	return fmt.Sprintf("Daily Digest\n\n🏋️ Coach\n%s\n\n😴 Sleep & Recovery\n%s\n\n💸 Finance\n%s", report.Coach, report.Sleep, report.Finance)
}
