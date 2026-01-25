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
	Coach string `json:"coach"`
	Sleep string `json:"sleep"`
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

	healthSummary, err := w.store.RecentHealthSummary(ctx, since)
	if err != nil {
		return err
	}
	metricsSummary, err := w.store.MetricsSummary(ctx, since)
	if err != nil {
		return err
	}
	prompt := fmt.Sprintf(`Ты дружелюбный ассистент и общаешься на русском в свободной форме.
Составь короткий неформальный дайджест в формате JSON.
Верни объект с ключами "coach" и "sleep".
- "coach" описывает тренировки и активность.
- "sleep" описывает сон и восстановление.
Используй данные ниже:
Заметки по тренировкам: %s
Заметки по метрикам: %s`, healthSummary, metricsSummary)
	jsonPayload, err := w.provider.GenerateJSON(ctx, prompt)
	if err != nil {
		return err
	}
	report, err := parseDailyReport(jsonPayload)
	if err != nil {
		return err
	}
	if w.chatID == 0 {
		return nil
	}
	message := FormatTelegramMessage(report)
	return w.sender.SendMessage(ctx, w.chatID, message)
}

func FormatTelegramMessage(report DailyReport) string {
	return fmt.Sprintf("Ежедневный отчёт\n\n🏋️ Тренер\n%s\n\n😴 Сон и восстановление\n%s", report.Coach, report.Sleep)
}

func parseDailyReport(payload string) (DailyReport, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return DailyReport{}, fmt.Errorf("parse report json: %w", err)
	}
	report := DailyReport{
		Coach: normalizeReportField(raw["coach"]),
		Sleep: normalizeReportField(raw["sleep"]),
	}
	return report, nil
}

func normalizeReportField(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
		return fmt.Sprint(v)
	}
}
