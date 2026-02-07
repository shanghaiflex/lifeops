package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"lifeops/internal/llm"
	"lifeops/internal/store"
)

type toolHandler func(ctx context.Context, args json.RawMessage) (string, error)

type toolRegistry struct {
	store       SummaryStore
	definitions []llm.ToolDefinition
	handlers    map[string]toolHandler
	timezone    *time.Location
	chatID      int64
	logChatIDs  []int64
}

func newToolRegistry(store SummaryStore, timezone *time.Location, nutritionLogChatIDs []int64) *toolRegistry {
	registry := &toolRegistry{
		store:      store,
		handlers:   make(map[string]toolHandler),
		timezone:   timezone,
		logChatIDs: append([]int64(nil), nutritionLogChatIDs...),
	}
	registry.register(llm.ToolDefinition{
		Name: "retrieve_workouts",
		Description: "Возвращает список тренировок за последние дни. Принимает параметры lookback_days (по умолчанию 7) " +
			"и limit (по умолчанию 10).",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"lookback_days", "limit"},
			"properties": map[string]any{
				"lookback_days": map[string]any{
					"type":        "integer",
					"description": "Сколько дней назад начинать поиск (1-30).",
					"minimum":     1,
					"maximum":     30,
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Максимум записей (1-50).",
					"minimum":     1,
					"maximum":     50,
				},
			},
		},
	}, func(ctx context.Context, raw json.RawMessage) (string, error) {
		var args rangeArgs
		if err := decodeArgs(raw, &args); err != nil {
			return "", err
		}
		args.applyDefaults(7, 10, 30, 50)
		since := time.Now().In(registry.timezone).AddDate(0, 0, -args.LookbackDays)
		workouts, err := registry.store.RecentWorkouts(ctx, since, args.Limit)
		if err != nil {
			return "", err
		}
		payload := map[string]any{
			"lookback_days": args.LookbackDays,
			"items":         serializeWorkoutsWithTZ(workouts, registry.timezone),
		}
		return marshalPayload(payload)
	})

	registry.register(llm.ToolDefinition{
		Name:        "retrieve_sleep_sessions",
		Description: "Возвращает эпизоды сна с разбивкой по фазам. Поддерживает lookback_days (по умолчанию 7) и limit (по умолчанию 10).",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"lookback_days", "limit"},
			"properties": map[string]any{
				"lookback_days": map[string]any{
					"type":        "integer",
					"description": "Сколько дней истории возвращать (1-30).",
					"minimum":     1,
					"maximum":     30,
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Максимум эпизодов (1-30).",
					"minimum":     1,
					"maximum":     30,
				},
			},
		},
	}, func(ctx context.Context, raw json.RawMessage) (string, error) {
		var args rangeArgs
		if err := decodeArgs(raw, &args); err != nil {
			return "", err
		}
		args.applyDefaults(7, 10, 30, 30)
		since := time.Now().In(registry.timezone).AddDate(0, 0, -args.LookbackDays)
		data, err := registry.store.RecentSleep(ctx, since, args.Limit)
		if err != nil {
			return "", err
		}
		payload := map[string]any{
			"lookback_days": args.LookbackDays,
			"items":         serializeSleepWithTZ(data, registry.timezone),
		}
		return marshalPayload(payload)
	})

	registry.register(llm.ToolDefinition{
		Name:        "retrieve_metrics",
		Description: "Возвращает свежие показатели здоровья (HRV, пульс покоя, шаги и т.д.). Принимает lookback_days, limit и список kinds.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"lookback_days", "limit", "kinds"},
			"properties": map[string]any{
				"lookback_days": map[string]any{
					"type":        "integer",
					"description": "Сколько дней анализировать (1-30).",
					"minimum":     1,
					"maximum":     30,
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Максимум записей (1-50).",
					"minimum":     1,
					"maximum":     50,
				},
				"kinds": map[string]any{
					"type":        "array",
					"description": "Фильтр по видам метрик. Доступные виды: hrv_sdnn (HRV), resting_heart_rate (пульс покоя), steps (шаги), active_energy (активная энергия).",
					"items": map[string]any{
						"type": "string",
					},
				},
			},
		},
	}, func(ctx context.Context, raw json.RawMessage) (string, error) {
		var args metricArgs
		if err := decodeArgs(raw, &args); err != nil {
			return "", err
		}
		args.rangeArgs.applyDefaults(7, 15, 30, 50)
		since := time.Now().In(registry.timezone).AddDate(0, 0, -args.LookbackDays)
		data, err := registry.store.RecentMetrics(ctx, since, args.Limit, args.Kinds)
		if err != nil {
			return "", err
		}
		payload := map[string]any{
			"lookback_days": args.LookbackDays,
			"kinds":         args.Kinds,
			"items":         serializeMetricsWithTZ(data, registry.timezone),
		}
		return marshalPayload(payload)
	})

	registry.register(llm.ToolDefinition{
		Name:        "retrieve_finance_summary",
		Description: "Возвращает текстовый обзор финансов за lookback_days (по умолчанию 7).",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"lookback_days"},
			"properties": map[string]any{
				"lookback_days": map[string]any{
					"type":        "integer",
					"description": "Сколько дней учитывать (1-30).",
					"minimum":     1,
					"maximum":     30,
				},
			},
		},
	}, func(ctx context.Context, raw json.RawMessage) (string, error) {
		var args struct {
			LookbackDays int `json:"lookback_days"`
		}
		if err := decodeArgs(raw, &args); err != nil {
			return "", err
		}
		if args.LookbackDays <= 0 {
			args.LookbackDays = 7
		}
		if args.LookbackDays > 30 {
			args.LookbackDays = 30
		}
		since := time.Now().In(registry.timezone).AddDate(0, 0, -args.LookbackDays)
		summary, err := registry.store.RecentFinanceSummary(ctx, since)
		if err != nil {
			return "", err
		}
		payload := map[string]any{
			"lookback_days": args.LookbackDays,
			"summary":       summary,
		}
		return marshalPayload(payload)
	})

	registry.register(llm.ToolDefinition{
		Name:        "retrieve_nutrition_logs",
		Description: "Возвращает последние записи о питании из чата нутрициониста. Используй перед анализом рациона.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"lookback_days", "limit"},
			"properties": map[string]any{
				"lookback_days": map[string]any{
					"type":        "integer",
					"description": "Сколько дней истории питания вернуть (1-30).",
					"minimum":     1,
					"maximum":     30,
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Максимум записей (1-50).",
					"minimum":     1,
					"maximum":     50,
				},
			},
		},
	}, func(ctx context.Context, raw json.RawMessage) (string, error) {
		var args rangeArgs
		if err := decodeArgs(raw, &args); err != nil {
			return "", err
		}
		args.applyDefaults(3, 20, 30, 50)
		since := time.Now().In(registry.timezone).AddDate(0, 0, -args.LookbackDays)
		targets := make([]int64, 0, len(registry.logChatIDs)+1)
		if registry.chatID != 0 {
			targets = append(targets, registry.chatID)
		}
		targets = append(targets, registry.logChatIDs...)
		if len(targets) == 0 {
			return "", fmt.Errorf("retrieve_nutrition_logs: chat context is required")
		}
		data, err := registry.loadNutritionEntries(ctx, targets, since, args.Limit)
		if err != nil {
			return "", err
		}
		payload := map[string]any{
			"lookback_days": args.LookbackDays,
			"items":         serializeNutritionEntries(data),
		}
		return marshalPayload(payload)
	})

	registry.register(llm.ToolDefinition{
		Name:        "retrieve_calendar_events",
		Description: "Возвращает события из календаря (Яндекс). Используй для планирования дня и учёта встреч.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"calendar_id", "lookback_days", "lookahead_days", "limit"},
			"properties": map[string]any{
				"calendar_id": map[string]any{
					"type":        "string",
					"description": "Идентификатор календаря (например, personal).",
				},
				"lookback_days": map[string]any{
					"type":        "integer",
					"description": "Сколько дней назад включать события (0-30).",
					"minimum":     0,
					"maximum":     30,
				},
				"lookahead_days": map[string]any{
					"type":        "integer",
					"description": "Сколько дней вперёд смотреть (1-60).",
					"minimum":     1,
					"maximum":     60,
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Максимум событий (1-100).",
					"minimum":     1,
					"maximum":     100,
				},
			},
		},
	}, func(ctx context.Context, raw json.RawMessage) (string, error) {
		var args calendarArgs
		if err := decodeArgs(raw, &args); err != nil {
			return "", err
		}
		args.applyDefaults()
		windowStart := time.Now().In(registry.timezone).AddDate(0, 0, -args.LookbackDays)
		windowEnd := time.Now().In(registry.timezone).AddDate(0, 0, args.LookaheadDays)
		events, err := registry.store.CalendarEventsBetween(ctx, strings.TrimSpace(args.CalendarID), windowStart, windowEnd, args.Limit)
		if err != nil {
			return "", err
		}
		payload := map[string]any{
			"calendar_id":  strings.TrimSpace(args.CalendarID),
			"window_start": windowStart.Format(time.RFC3339),
			"window_end":   windowEnd.Format(time.RFC3339),
			"items":        serializeCalendarEvents(events, registry.timezone),
		}
		return marshalPayload(payload)
	})

	return registry
}

func (t *toolRegistry) register(def llm.ToolDefinition, handler toolHandler) {
	if _, exists := t.handlers[def.Name]; exists {
		return
	}
	t.definitions = append(t.definitions, def)
	t.handlers[def.Name] = handler
}

func (t *toolRegistry) Definitions() []llm.ToolDefinition {
	out := make([]llm.ToolDefinition, len(t.definitions))
	copy(out, t.definitions)
	return out
}

func (t *toolRegistry) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	handler, ok := t.handlers[name]
	if !ok {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	return handler(ctx, args)
}

func (t *toolRegistry) Describe() string {
	if len(t.definitions) == 0 {
		return ""
	}
	parts := make([]string, len(t.definitions))
	for i, def := range t.definitions {
		parts[i] = fmt.Sprintf("%s — %s", def.Name, def.Description)
	}
	return strings.Join(parts, "; ")
}

func (t *toolRegistry) SetChatContext(chatID int64) {
	t.chatID = chatID
}

type rangeArgs struct {
	LookbackDays int `json:"lookback_days"`
	Limit        int `json:"limit"`
}

func (r *rangeArgs) applyDefaults(defaultLookback, defaultLimit, maxLookback, maxLimit int) {
	if r.LookbackDays <= 0 {
		r.LookbackDays = defaultLookback
	}
	if r.LookbackDays > maxLookback {
		r.LookbackDays = maxLookback
	}
	if r.Limit <= 0 {
		r.Limit = defaultLimit
	}
	if r.Limit > maxLimit {
		r.Limit = maxLimit
	}
}

type metricArgs struct {
	rangeArgs
	Kinds []string `json:"kinds"`
}

type calendarArgs struct {
	CalendarID    string `json:"calendar_id"`
	LookbackDays  int    `json:"lookback_days"`
	LookaheadDays int    `json:"lookahead_days"`
	Limit         int    `json:"limit"`
}

func (c *calendarArgs) applyDefaults() {
	c.CalendarID = strings.TrimSpace(c.CalendarID)
	if c.LookbackDays < 0 {
		c.LookbackDays = 0
	}
	if c.LookbackDays > 30 {
		c.LookbackDays = 30
	}
	if c.LookaheadDays <= 0 {
		c.LookaheadDays = 7
	}
	if c.LookaheadDays > 60 {
		c.LookaheadDays = 60
	}
	if c.Limit <= 0 {
		c.Limit = 20
	}
	if c.Limit > 100 {
		c.Limit = 100
	}
}

func decodeArgs(raw json.RawMessage, target interface{}) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, target)
}

func marshalPayload(payload any) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func serializeWorkouts(workouts []store.Workout) []map[string]any {
	return serializeWorkoutsWithTZ(workouts, time.UTC)
}

func serializeWorkoutsWithTZ(workouts []store.Workout, loc *time.Location) []map[string]any {
	if loc == nil {
		loc = time.UTC
	}
	out := make([]map[string]any, 0, len(workouts))
	for _, w := range workouts {
		start := w.Start.In(loc)
		end := w.End.In(loc)
		item := map[string]any{
			"id":               w.ID,
			"type":             w.WorkoutType,
			"start":            start.Format(time.RFC3339),
			"end":              end.Format(time.RFC3339),
			"duration_minutes": w.DurationMinutes,
		}
		if w.DistanceMeters != nil {
			item["distance_meters"] = *w.DistanceMeters
		}
		if w.Calories != nil {
			item["calories"] = *w.Calories
		}
		if w.AverageHeartRate != nil {
			item["average_heart_rate"] = *w.AverageHeartRate
		}
		out = append(out, item)
	}
	return out
}

func serializeSleep(sessions []store.Sleep) []map[string]any {
	return serializeSleepWithTZ(sessions, time.UTC)
}

func serializeSleepWithTZ(sessions []store.Sleep, loc *time.Location) []map[string]any {
	if loc == nil {
		loc = time.UTC
	}
	out := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		start := s.Start.In(loc)
		end := s.End.In(loc)
		item := map[string]any{
			"id":            s.ID,
			"start":         start.Format(time.RFC3339),
			"end":           end.Format(time.RFC3339),
			"total_minutes": s.TotalMinutes,
			"breakdown": map[string]any{
				"rem":   s.RemMinutes,
				"deep":  s.DeepMinutes,
				"core":  s.CoreMinutes,
				"awake": s.AwakeMinutes,
			},
		}
		out = append(out, item)
	}
	return out
}

func serializeMetrics(metrics []store.Metric) []map[string]any {
	return serializeMetricsWithTZ(metrics, time.UTC)
}

func serializeMetricsWithTZ(metrics []store.Metric, loc *time.Location) []map[string]any {
	if loc == nil {
		loc = time.UTC
	}
	out := make([]map[string]any, 0, len(metrics))
	for _, m := range metrics {
		start := m.Start.In(loc)
		end := m.End.In(loc)
		item := map[string]any{
			"id":    m.ID,
			"kind":  m.Kind,
			"value": m.Value,
			"unit":  m.Unit,
			"start": start.Format(time.RFC3339),
			"end":   end.Format(time.RFC3339),
		}
		out = append(out, item)
	}
	return out
}

func (t *toolRegistry) loadNutritionEntries(ctx context.Context, chatIDs []int64, since time.Time, limit int) ([]store.NutritionEntry, error) {
	if len(chatIDs) == 0 {
		return nil, nil
	}
	unique := make(map[int64]struct{}, len(chatIDs))
	var combined []store.NutritionEntry
	for _, id := range chatIDs {
		if _, ok := unique[id]; ok {
			continue
		}
		unique[id] = struct{}{}
		data, err := t.store.RecentNutritionEntries(ctx, id, since, limit)
		if err != nil {
			return nil, err
		}
		combined = append(combined, data...)
	}
	sort.Slice(combined, func(i, j int) bool {
		return combined[i].MessageTS.After(combined[j].MessageTS)
	})
	if len(combined) > limit {
		combined = combined[:limit]
	}
	return combined, nil
}

func serializeNutritionEntries(entries []store.NutritionEntry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		item := map[string]any{
			"id":            entry.ID,
			"summary":       entry.Summary,
			"original_text": entry.OriginalText,
			"message_ts":    entry.MessageTS.Format(time.RFC3339),
		}
		if entry.Calories != nil {
			item["calories"] = *entry.Calories
		}
		if entry.ProteinGrams != nil {
			item["protein_g"] = *entry.ProteinGrams
		}
		if entry.CarbsGrams != nil {
			item["carbs_g"] = *entry.CarbsGrams
		}
		if entry.FatGrams != nil {
			item["fat_g"] = *entry.FatGrams
		}
		if strings.TrimSpace(entry.PhotoFileID) != "" {
			item["photo_file_id"] = entry.PhotoFileID
		}
		out = append(out, item)
	}
	return out
}

func serializeCalendarEvents(events []store.CalendarEvent, loc *time.Location) []map[string]any {
	out := make([]map[string]any, 0, len(events))
	if loc == nil {
		loc = time.UTC
	}
	for _, ev := range events {
		start := ev.Start.In(loc)
		end := ev.End.In(loc)
		item := map[string]any{
			"id":             ev.ID,
			"calendar_id":    ev.CalendarID,
			"event_uid":      ev.EventUID,
			"title":          ev.Title,
			"start":          start.Format(time.RFC3339),
			"end":            end.Format(time.RFC3339),
			"all_day":        ev.AllDay,
			"duration_hours": end.Sub(start).Hours(),
		}
		if strings.TrimSpace(ev.Description) != "" {
			item["description"] = ev.Description
		}
		if strings.TrimSpace(ev.Location) != "" {
			item["location"] = ev.Location
		}
		if ev.SourceUpdatedAt != nil {
			item["source_updated_at"] = ev.SourceUpdatedAt.In(loc).Format(time.RFC3339)
		}
		out = append(out, item)
	}
	return out
}
