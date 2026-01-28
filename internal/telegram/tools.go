package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"lifeops/internal/config"
	"lifeops/internal/llm"
	"lifeops/internal/store"
)

// universalToolExecutor provides all tools to all agents in interactive chat
type universalToolExecutor struct {
	store      *store.Store
	timezone   *time.Location
	logChatIDs []int64
}

func newUniversalToolExecutor(store *store.Store, agentCfg *config.AgentConfig) toolExecutor {
	if store == nil {
		return nil
	}
	tz := time.UTC
	if agentCfg != nil && agentCfg.Timezone != nil {
		tz = agentCfg.Timezone
	}
	var logIDs []int64
	if agentCfg != nil && agentCfg.NutritionLogChatID != 0 {
		logIDs = append(logIDs, agentCfg.NutritionLogChatID)
	}
	return &universalToolExecutor{
		store:      store,
		timezone:   tz,
		logChatIDs: logIDs,
	}
}

func (u *universalToolExecutor) Definitions() []llm.ToolDefinition {
	return []llm.ToolDefinition{
		{
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
		},
		{
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
		},
		{
			Name:        "retrieve_metrics",
			Description: "Возвращает свежие показатели здоровья (HRV, пульс и т.д.). Принимает lookback_days, limit и список kinds.",
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
						"description": "Фильтр по видам метрик (например, HRV, resting_hr).",
						"items": map[string]any{
							"type": "string",
						},
					},
				},
			},
		},
		{
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
		},
		{
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
		},
		{
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
		},
	}
}

func (u *universalToolExecutor) Execute(ctx context.Context, chatID int64, name string, args json.RawMessage) (string, error) {
	switch name {
	case "retrieve_workouts":
		return u.retrieveWorkouts(ctx, args)
	case "retrieve_sleep_sessions":
		return u.retrieveSleepSessions(ctx, args)
	case "retrieve_metrics":
		return u.retrieveMetrics(ctx, args)
	case "retrieve_nutrition_logs":
		return u.retrieveNutritionLogs(ctx, chatID, args)
	case "retrieve_calendar_events":
		return u.retrieveCalendarEvents(ctx, args)
	case "retrieve_finance_summary":
		return u.retrieveFinanceSummary(ctx, args)
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
}

func (u *universalToolExecutor) retrieveWorkouts(ctx context.Context, raw json.RawMessage) (string, error) {
	var args rangeArgs
	if err := decodeToolArgs(raw, &args); err != nil {
		return "", err
	}
	args.applyDefaults(7, 10, 30, 50)
	since := time.Now().In(u.timezone).AddDate(0, 0, -args.LookbackDays)
	workouts, err := u.store.RecentWorkouts(ctx, since, args.Limit)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"lookback_days": args.LookbackDays,
		"items":         serializeWorkoutsWithTZ(workouts, u.timezone),
	}
	return marshalToolPayload(payload)
}

func (u *universalToolExecutor) retrieveSleepSessions(ctx context.Context, raw json.RawMessage) (string, error) {
	var args rangeArgs
	if err := decodeToolArgs(raw, &args); err != nil {
		return "", err
	}
	args.applyDefaults(7, 10, 30, 30)
	since := time.Now().In(u.timezone).AddDate(0, 0, -args.LookbackDays)
	data, err := u.store.RecentSleep(ctx, since, args.Limit)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"lookback_days": args.LookbackDays,
		"items":         serializeSleepWithTZ(data, u.timezone),
	}
	return marshalToolPayload(payload)
}

func (u *universalToolExecutor) retrieveMetrics(ctx context.Context, raw json.RawMessage) (string, error) {
	var args metricArgs
	if err := decodeToolArgs(raw, &args); err != nil {
		return "", err
	}
	args.rangeArgs.applyDefaults(7, 15, 30, 50)
	since := time.Now().In(u.timezone).AddDate(0, 0, -args.LookbackDays)
	data, err := u.store.RecentMetrics(ctx, since, args.Limit, args.Kinds)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"lookback_days": args.LookbackDays,
		"kinds":         args.Kinds,
		"items":         serializeMetricsWithTZ(data, u.timezone),
	}
	return marshalToolPayload(payload)
}

func (u *universalToolExecutor) retrieveNutritionLogs(ctx context.Context, chatID int64, raw json.RawMessage) (string, error) {
	var args rangeArgs
	if err := decodeToolArgs(raw, &args); err != nil {
		return "", err
	}
	args.applyDefaults(3, 20, 30, 50)
	since := time.Now().In(u.timezone).AddDate(0, 0, -args.LookbackDays)
	targets := make([]int64, 0, len(u.logChatIDs)+1)
	if chatID != 0 {
		targets = append(targets, chatID)
	}
	targets = append(targets, u.logChatIDs...)
	if len(targets) == 0 {
		return "", fmt.Errorf("retrieve_nutrition_logs: chat context is required")
	}
	data, err := u.loadNutritionEntries(ctx, targets, since, args.Limit)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"lookback_days": args.LookbackDays,
		"items":         serializeNutritionEntries(data),
	}
	return marshalToolPayload(payload)
}

func (u *universalToolExecutor) retrieveCalendarEvents(ctx context.Context, raw json.RawMessage) (string, error) {
	var args calendarArgs
	if err := decodeToolArgs(raw, &args); err != nil {
		return "", err
	}
	args.applyDefaults()
	windowStart := time.Now().In(u.timezone).AddDate(0, 0, -args.LookbackDays)
	windowEnd := time.Now().In(u.timezone).AddDate(0, 0, args.LookaheadDays)
	events, err := u.store.CalendarEventsBetween(ctx, strings.TrimSpace(args.CalendarID), windowStart, windowEnd, args.Limit)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"calendar_id":  strings.TrimSpace(args.CalendarID),
		"window_start": windowStart.Format(time.RFC3339),
		"window_end":   windowEnd.Format(time.RFC3339),
		"items":        serializeCalendarEvents(events, u.timezone),
	}
	return marshalToolPayload(payload)
}

func (u *universalToolExecutor) retrieveFinanceSummary(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		LookbackDays int `json:"lookback_days"`
	}
	if err := decodeToolArgs(raw, &args); err != nil {
		return "", err
	}
	if args.LookbackDays <= 0 {
		args.LookbackDays = 7
	}
	if args.LookbackDays > 30 {
		args.LookbackDays = 30
	}
	since := time.Now().In(u.timezone).AddDate(0, 0, -args.LookbackDays)
	summary, err := u.store.RecentFinanceSummary(ctx, since)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"lookback_days": args.LookbackDays,
		"summary":       summary,
	}
	return marshalToolPayload(payload)
}

func (u *universalToolExecutor) loadNutritionEntries(ctx context.Context, chatIDs []int64, since time.Time, limit int) ([]store.NutritionEntry, error) {
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
		data, err := u.store.RecentNutritionEntries(ctx, id, since, limit)
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
			"duration_hours": math.Round(end.Sub(start).Hours()*100) / 100,
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
