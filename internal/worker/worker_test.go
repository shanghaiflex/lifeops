package worker

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"lifeops/internal/config"
	"lifeops/internal/llm"
	"lifeops/internal/store"
)

type fakeStore struct {
	healthSummary  string
	metricsSummary string
	financeSummary string
	history        map[int64][]store.ChatMessage
	chatIDs        []int64
	savedMessages  []savedMessage
}

type savedMessage struct {
	chatID  int64
	agent   string
	role    string
	content string
}

func (f *fakeStore) RecentHealthSummary(_ context.Context, _ time.Time) (string, error) {
	if f.healthSummary != "" {
		return f.healthSummary, nil
	}
	return "Health ok", nil
}

func (f *fakeStore) MetricsSummary(_ context.Context, _ time.Time) (string, error) {
	if f.metricsSummary != "" {
		return f.metricsSummary, nil
	}
	return "Metrics ok", nil
}

func (f *fakeStore) RecentFinanceSummary(_ context.Context, _ time.Time) (string, error) {
	if f.financeSummary != "" {
		return f.financeSummary, nil
	}
	return "Finance ok", nil
}

func (f *fakeStore) ChatHistory(_ context.Context, chatID int64, _ string, _ int) ([]store.ChatMessage, error) {
	if f.history == nil {
		return nil, nil
	}
	return f.history[chatID], nil
}

func (f *fakeStore) SaveChatMessage(_ context.Context, chatID int64, agent string, role string, content string) error {
	f.savedMessages = append(f.savedMessages, savedMessage{
		chatID:  chatID,
		agent:   agent,
		role:    role,
		content: content,
	})
	return nil
}

func (f *fakeStore) ChatIDsForAgent(_ context.Context, _ string) ([]int64, error) {
	return f.chatIDs, nil
}

type mockSender struct {
	sent []sentMessage
}

type sentMessage struct {
	chatID int64
	text   string
}

func (m *mockSender) SendMessage(_ context.Context, chatID int64, text string) error {
	m.sent = append(m.sent, sentMessage{chatID: chatID, text: text})
	return nil
}

func TestWorkerRunOnceSendsDailyDigest(t *testing.T) {
	provider := &llm.MockProvider{ChatResponse: "LLM daily reply"}
	sender := &mockSender{}
	testStore := &fakeStore{
		healthSummary:  "Health summary",
		metricsSummary: "Metrics summary",
		financeSummary: "Finance summary",
		history: map[int64][]store.ChatMessage{
			100: {
				{Role: "user", Content: "Previous question"},
			},
		},
		chatIDs: []int64{100},
	}
	agent := config.AgentConfig{
		Name:              "coach",
		Prompt:            "Act as helpful coach",
		DailyReviewPrompt: "H: {health_summary} M: {metrics_summary} F: {finance_summary}",
		Timezone:          time.UTC,
		DailyReviewTime:   config.DailyReviewTime{Hour: 9, Minute: 0},
	}

	worker := New(testStore, provider, sender, agent, 5, nil)
	require.NoError(t, worker.RunOnce(context.Background()))

	require.Len(t, sender.sent, 1)
	require.Equal(t, int64(100), sender.sent[0].chatID)
	require.Equal(t, "LLM daily reply", sender.sent[0].text)
	require.Len(t, testStore.savedMessages, 2)
	require.Equal(t, "user", testStore.savedMessages[0].role)
	require.Contains(t, testStore.savedMessages[0].content, "Health summary")
	require.Equal(t, "assistant", testStore.savedMessages[1].role)
	require.Equal(t, "LLM daily reply", testStore.savedMessages[1].content)
}

func TestWorkerRunOnceUsesDefaultChatIDs(t *testing.T) {
	provider := &llm.MockProvider{ChatResponse: "Daily digest"}
	sender := &mockSender{}
	testStore := &fakeStore{}
	agent := config.AgentConfig{
		Name:            "coach",
		Prompt:          "Prompt",
		Timezone:        time.UTC,
		DailyReviewTime: config.DailyReviewTime{Hour: 9, Minute: 0},
	}

	worker := New(testStore, provider, sender, agent, 5, []int64{42})
	require.NoError(t, worker.RunOnce(context.Background()))
	require.Len(t, sender.sent, 1)
	require.Equal(t, int64(42), sender.sent[0].chatID)
}

func TestRenderDailyReviewPrompt(t *testing.T) {
	out := renderDailyReviewPrompt(
		"Health={health_summary} Metrics={metrics_summary} Finance={finance_summary}",
		"H",
		"M",
		"F",
	)
	require.Equal(t, "Health=H Metrics=M Finance=F", out)
}
