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
	calendarEvents []store.CalendarEvent
	nutrition      map[int64][]store.NutritionEntry
	financeSummary string
	history        map[int64][]store.ChatMessage
	chatIDs        []int64
	savedMessages  []savedMessage
	nutritionCalls []int64
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

func (f *fakeStore) CalendarEventsBetween(_ context.Context, _ string, _ time.Time, _ time.Time, limit int) ([]store.CalendarEvent, error) {
	if limit > 0 && len(f.calendarEvents) > limit {
		return f.calendarEvents[:limit], nil
	}
	return f.calendarEvents, nil
}

func (f *fakeStore) RecentNutritionEntries(_ context.Context, chatID int64, _ time.Time, limit int) ([]store.NutritionEntry, error) {
	f.nutritionCalls = append(f.nutritionCalls, chatID)
	if f.nutrition == nil {
		return nil, nil
	}
	entries := f.nutrition[chatID]
	if limit > 0 && len(entries) > limit {
		return entries[:limit], nil
	}
	return entries, nil
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

func TestWorkerNutritionToolUsesChatContext(t *testing.T) {
	provider := &llm.MockProvider{
		Responses: []llm.MockChatResponse{
			{
				ToolCalls: []llm.ToolCall{
					{ID: "tool-1", Name: "retrieve_nutrition_logs", Arguments: json.RawMessage(`{"lookback_days":2,"limit":5}`)},
				},
			},
			{
				Content: "Nutrition summary",
			},
		},
	}
	sender := &mockSender{}
	entry := store.NutritionEntry{
		ID:           1,
		ChatID:       555,
		MessageTS:    time.Now(),
		OriginalText: "каша с орехами",
		Summary:      "Овсянка с орехами",
	}
	testStore := &fakeStore{
		chatIDs: []int64{555},
		nutrition: map[int64][]store.NutritionEntry{
			555: {entry},
		},
	}
	agent := config.AgentConfig{
		Name:            "coach",
		Prompt:          "Prompt",
		Timezone:        time.UTC,
		DailyReviewTime: config.DailyReviewTime{Hour: 9, Minute: 0},
	}

	worker := New(testStore, provider, sender, agent, 5, nil)
	require.NoError(t, worker.RunOnce(context.Background()))
	require.Len(t, testStore.nutritionCalls, 1)
	require.Equal(t, int64(555), testStore.nutritionCalls[0])
	require.Len(t, sender.sent, 1)
	require.Equal(t, "Nutrition summary", sender.sent[0].text)
}

func TestWorkerNutritionToolUsesConfiguredLogID(t *testing.T) {
	provider := &llm.MockProvider{
		Responses: []llm.MockChatResponse{
			{
				ToolCalls: []llm.ToolCall{
					{ID: "tool-1", Name: "retrieve_nutrition_logs", Arguments: json.RawMessage(`{"lookback_days":2,"limit":5}`)},
				},
			},
			{
				Content: "Nutrition summary",
			},
		},
	}
	sender := &mockSender{}
	logChatID := int64(123)
	reviewChatID := int64(777)
	entry := store.NutritionEntry{
		ID:           1,
		ChatID:       logChatID,
		MessageTS:    time.Now(),
		OriginalText: "яичница",
		Summary:      "Яичница с овощами",
	}
	testStore := &fakeStore{
		chatIDs: []int64{reviewChatID},
		nutrition: map[int64][]store.NutritionEntry{
			logChatID: {entry},
		},
	}
	agent := config.AgentConfig{
		Name:                  "nutrition",
		Prompt:                "Prompt",
		Timezone:              time.UTC,
		DailyReviewTime:       config.DailyReviewTime{Hour: 9, Minute: 0},
		NutritionLogChatID:    logChatID,
		NutritionReviewChatID: reviewChatID,
	}
	worker := New(testStore, provider, sender, agent, 5, nil)
	require.NoError(t, worker.RunOnce(context.Background()))
	require.Equal(t, []int64{reviewChatID, logChatID}, testStore.nutritionCalls)
	require.Len(t, sender.sent, 1)
	require.Equal(t, reviewChatID, sender.sent[0].chatID)
	require.Equal(t, "Nutrition summary", sender.sent[0].text)
}

func TestWorkerCalendarEventsTool(t *testing.T) {
	now := time.Now()
	provider := &llm.MockProvider{
		Responses: []llm.MockChatResponse{
			{
				ToolCalls: []llm.ToolCall{
					{
						ID:   "tool-1",
						Name: "retrieve_calendar_events",
						Arguments: json.RawMessage(`{
							"calendar_id":"personal",
							"lookback_days":1,
							"lookahead_days":3,
							"limit":5
						}`),
					},
				},
			},
			{
				Content: "Calendar summary",
			},
		},
	}
	sender := &mockSender{}
	testStore := &fakeStore{
		chatIDs: []int64{101},
		calendarEvents: []store.CalendarEvent{
			{
				ID:         "evt-1",
				CalendarID: "personal",
				EventUID:   "uid-1",
				Title:      "Demo call",
				Start:      now,
				End:        now.Add(30 * time.Minute),
			},
		},
	}
	agent := config.AgentConfig{
		Name:            "coach",
		Prompt:          "Prompt",
		Timezone:        time.UTC,
		DailyReviewTime: config.DailyReviewTime{Hour: 9, Minute: 0},
	}
	worker := New(testStore, provider, sender, agent, 5, nil)
	require.NoError(t, worker.RunOnce(context.Background()))
	require.Len(t, sender.sent, 1)
	require.Equal(t, "Calendar summary", sender.sent[0].text)
}

func TestWorkerRunOnceUsesNutritionReviewChatID(t *testing.T) {
	provider := &llm.MockProvider{ChatResponse: "Daily review"}
	sender := &mockSender{}
	testStore := &fakeStore{
		chatIDs: []int64{111},
	}
	agent := config.AgentConfig{
		Name:                  "nutrition",
		Prompt:                "Prompt",
		Timezone:              time.UTC,
		DailyReviewTime:       config.DailyReviewTime{Hour: 9, Minute: 0},
		NutritionLogChatID:    111,
		NutritionReviewChatID: 777,
	}
	worker := New(testStore, provider, sender, agent, 5, nil)
	require.NoError(t, worker.RunOnce(context.Background()))
	require.Len(t, sender.sent, 1)
	require.Equal(t, int64(777), sender.sent[0].chatID)
	require.Equal(t, "Daily review", sender.sent[0].text)
}

func TestWorkerRunForChatTargetsSingleChat(t *testing.T) {
	provider := &llm.MockProvider{ChatResponse: "On-demand review"}
	sender := &mockSender{}
	testStore := &fakeStore{
		history: map[int64][]store.ChatMessage{},
	}
	agent := config.AgentConfig{
		Name:            "nutrition",
		Prompt:          "Prompt",
		Timezone:        time.UTC,
		DailyReviewTime: config.DailyReviewTime{Hour: 9, Minute: 0},
	}

	worker := New(testStore, provider, sender, agent, 5, nil)
	require.NoError(t, worker.RunForChat(context.Background(), 777))
	require.Len(t, sender.sent, 1)
	require.Equal(t, int64(777), sender.sent[0].chatID)
	require.Equal(t, "On-demand review", sender.sent[0].text)
}

func TestWorkerRunForChatRequiresChatID(t *testing.T) {
	provider := &llm.MockProvider{}
	sender := &mockSender{}
	testStore := &fakeStore{}
	agent := config.AgentConfig{
		Name:            "nutrition",
		Prompt:          "Prompt",
		Timezone:        time.UTC,
		DailyReviewTime: config.DailyReviewTime{Hour: 9, Minute: 0},
	}

	worker := New(testStore, provider, sender, agent, 5, nil)
	require.Error(t, worker.RunForChat(context.Background(), 0))
}

func TestNormalizeReviewTimes(t *testing.T) {
	times := []config.DailyReviewTime{
		{Hour: 21, Minute: 0},
		{Hour: 9, Minute: 0},
		{Hour: 21, Minute: 0},
		{Hour: -1, Minute: 0},
	}
	normalized := normalizeReviewTimes(times)
	require.Equal(t, []config.DailyReviewTime{
		{Hour: 9, Minute: 0},
		{Hour: 21, Minute: 0},
	}, normalized)
}

func TestWorkerNextReviewTime(t *testing.T) {
	loc := time.FixedZone("MSK", 3*3600)
	w := &Worker{
		agent: config.AgentConfig{
			Timezone: loc,
		},
		reviewTimes: []config.DailyReviewTime{
			{Hour: 9, Minute: 0},
			{Hour: 21, Minute: 0},
		},
	}
	morning := time.Date(2026, 1, 27, 8, 0, 0, 0, loc)
	next := w.nextReviewTime(morning, true)
	require.Equal(t, time.Date(2026, 1, 27, 9, 0, 0, 0, loc), next)

	afterMorning := time.Date(2026, 1, 27, 10, 0, 0, 0, loc)
	evening := w.nextReviewTime(afterMorning, true)
	require.Equal(t, time.Date(2026, 1, 27, 21, 0, 0, 0, loc), evening)

	afterEvening := time.Date(2026, 1, 27, 22, 0, 0, 0, loc)
	tomorrow := w.nextReviewTime(afterEvening, true)
	require.Equal(t, time.Date(2026, 1, 28, 9, 0, 0, 0, loc), tomorrow)
}
