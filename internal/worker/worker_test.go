package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"lifeops/internal/config"
	"lifeops/internal/llm"
	"lifeops/internal/store"
)

type fakeStore struct {
	workouts       []store.Workout
	sleep          []store.Sleep
	metrics        []store.Metric
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

func (f *fakeStore) RecentFinanceSummary(_ context.Context, _ time.Time) (string, error) {
	if f.financeSummary != "" {
		return f.financeSummary, nil
	}
	return "Finance ok", nil
}

func (f *fakeStore) RecentWorkouts(_ context.Context, _ time.Time, limit int) ([]store.Workout, error) {
	if limit > 0 && len(f.workouts) > limit {
		return f.workouts[:limit], nil
	}
	return f.workouts, nil
}

func (f *fakeStore) RecentSleep(_ context.Context, _ time.Time, limit int) ([]store.Sleep, error) {
	if limit > 0 && len(f.sleep) > limit {
		return f.sleep[:limit], nil
	}
	return f.sleep, nil
}

func (f *fakeStore) RecentMetrics(_ context.Context, _ time.Time, limit int, _ []string) ([]store.Metric, error) {
	if limit > 0 && len(f.metrics) > limit {
		return f.metrics[:limit], nil
	}
	return f.metrics, nil
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

func TestWorkerRunOnceUsesTools(t *testing.T) {
	provider := &llm.MockProvider{
		Responses: []llm.MockChatResponse{
			{
				ToolCalls: []llm.ToolCall{
					{ID: "call-1", Name: "retrieve_workouts", Arguments: json.RawMessage(`{"lookback_days":3}`)},
				},
			},
			{
				Content: "LLM daily reply",
			},
		},
	}
	sender := &mockSender{}
	testStore := &fakeStore{
		workouts: []store.Workout{
			{ID: "w1", WorkoutType: "run", DurationMinutes: 45, Start: time.Now(), End: time.Now().Add(45 * time.Minute)},
		},
		history: map[int64][]store.ChatMessage{
			100: {
				{Role: "user", Content: "Previous question"},
			},
		},
		chatIDs: []int64{100},
	}
	agent := config.AgentConfig{
		Name:            "coach",
		Prompt:          "Act as helpful coach",
		Timezone:        time.UTC,
		DailyReviewTime: config.DailyReviewTime{Hour: 9, Minute: 0},
	}

	worker := New(testStore, provider, sender, agent, 5, nil)
	require.NoError(t, worker.RunOnce(context.Background()))

	require.Len(t, provider.ChatRequests, 2)
	require.Len(t, sender.sent, 1)
	require.Equal(t, int64(100), sender.sent[0].chatID)
	require.Equal(t, "LLM daily reply", sender.sent[0].text)
	require.Len(t, testStore.savedMessages, 2)
	require.Equal(t, "user", testStore.savedMessages[0].role)
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
	out := renderDailyReviewPrompt("  Prompt text  ")
	require.Equal(t, "Prompt text", out)
}
