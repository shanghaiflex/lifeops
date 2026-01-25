package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"lifeops/internal/llm"
)

type fakeStore struct{}

func (f *fakeStore) RecentHealthSummary(_ context.Context, _ time.Time) (string, error) {
	return "Health ok", nil
}

func (f *fakeStore) MetricsSummary(_ context.Context, _ time.Time) (string, error) {
	return "Metrics ok", nil
}

func (f *fakeStore) RecentFinanceSummary(_ context.Context, _ time.Time) (string, error) {
	return "Finance ok", nil
}

type mockSender struct {
	lastMessage string
}

func (m *mockSender) SendMessage(_ context.Context, _ int64, text string) error {
	m.lastMessage = text
	return nil
}

func TestWorkerRunOnceUsesMockProvider(t *testing.T) {
	provider := &llm.MockProvider{}
	sender := &mockSender{}
	report := DailyReport{Coach: "Coach text", Sleep: "Sleep text", Finance: "Finance text"}
	payload, err := json.Marshal(report)
	require.NoError(t, err)
	provider.JSONResponse = string(payload)

	w := New(&fakeStore{}, provider, sender, time.UTC, 123)
	require.NoError(t, w.RunOnce(context.Background()))
	require.Contains(t, sender.lastMessage, "Daily Digest")
	require.Contains(t, sender.lastMessage, "Coach text")
}

func TestFormatTelegramMessageSnapshot(t *testing.T) {
	report := DailyReport{Coach: "Coach", Sleep: "Sleep", Finance: "Finance"}
	message := FormatTelegramMessage(report)
	require.Equal(t, "Daily Digest\n\n🏋️ Coach\nCoach\n\n😴 Sleep & Recovery\nSleep\n\n💸 Finance\nFinance", message)
}
