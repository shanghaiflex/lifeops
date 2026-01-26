package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"lifeops/internal/llm"
	"lifeops/internal/store"
)

type toolHandler func(ctx context.Context, args json.RawMessage) (string, error)

type toolRegistry struct {
	definitions []llm.ToolDefinition
	handlers    map[string]toolHandler
	timezone    *time.Location
}

func newToolRegistry(store SummaryStore, timezone *time.Location) *toolRegistry {
	registry := &toolRegistry{
		handlers: make(map[string]toolHandler),
		timezone: timezone,
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
		since := time.Now().In(timezone).AddDate(0, 0, -args.LookbackDays)
		workouts, err := store.RecentWorkouts(ctx, since, args.Limit)
		if err != nil {
			return "", err
		}
		payload := map[string]any{
			"lookback_days": args.LookbackDays,
			"items":         serializeWorkouts(workouts),
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
		since := time.Now().In(timezone).AddDate(0, 0, -args.LookbackDays)
		data, err := store.RecentSleep(ctx, since, args.Limit)
		if err != nil {
			return "", err
		}
		payload := map[string]any{
			"lookback_days": args.LookbackDays,
			"items":         serializeSleep(data),
		}
		return marshalPayload(payload)
	})

	registry.register(llm.ToolDefinition{
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
	}, func(ctx context.Context, raw json.RawMessage) (string, error) {
		var args metricArgs
		if err := decodeArgs(raw, &args); err != nil {
			return "", err
		}
		args.rangeArgs.applyDefaults(7, 15, 30, 50)
		since := time.Now().In(timezone).AddDate(0, 0, -args.LookbackDays)
		data, err := store.RecentMetrics(ctx, since, args.Limit, args.Kinds)
		if err != nil {
			return "", err
		}
		payload := map[string]any{
			"lookback_days": args.LookbackDays,
			"kinds":         args.Kinds,
			"items":         serializeMetrics(data),
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
		since := time.Now().In(timezone).AddDate(0, 0, -args.LookbackDays)
		summary, err := store.RecentFinanceSummary(ctx, since)
		if err != nil {
			return "", err
		}
		payload := map[string]any{
			"lookback_days": args.LookbackDays,
			"summary":       summary,
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
	out := make([]map[string]any, 0, len(workouts))
	for _, w := range workouts {
		item := map[string]any{
			"id":               w.ID,
			"type":             w.WorkoutType,
			"start":            w.Start.Format(time.RFC3339),
			"end":              w.End.Format(time.RFC3339),
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
	out := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		item := map[string]any{
			"id":            s.ID,
			"start":         s.Start.Format(time.RFC3339),
			"end":           s.End.Format(time.RFC3339),
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
	out := make([]map[string]any, 0, len(metrics))
	for _, m := range metrics {
		item := map[string]any{
			"id":    m.ID,
			"kind":  m.Kind,
			"value": m.Value,
			"unit":  m.Unit,
			"start": m.Start.Format(time.RFC3339),
			"end":   m.End.Format(time.RFC3339),
		}
		out = append(out, item)
	}
	return out
}
