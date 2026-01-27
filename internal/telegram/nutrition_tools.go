package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"lifeops/internal/config"
	"lifeops/internal/llm"
	"lifeops/internal/store"
)

type toolExecutor interface {
	Definitions() []llm.ToolDefinition
	Execute(ctx context.Context, chatID int64, name string, args json.RawMessage) (string, error)
}

func newNutritionToolExecutor(store *store.Store, agentCfg *config.AgentConfig) toolExecutor {
	if store == nil || agentCfg == nil || !strings.EqualFold(agentCfg.Name, "nutrition") {
		return nil
	}
	tz := agentCfg.Timezone
	if tz == nil {
		tz = time.UTC
	}
	var logIDs []int64
	if agentCfg.NutritionLogChatID != 0 {
		logIDs = append(logIDs, agentCfg.NutritionLogChatID)
	}
	return &nutritionToolExecutor{
		store:      store,
		timezone:   tz,
		logChatIDs: logIDs,
	}
}

type nutritionToolExecutor struct {
	store      *store.Store
	timezone   *time.Location
	logChatIDs []int64
}

func (n *nutritionToolExecutor) Definitions() []llm.ToolDefinition {
	return []llm.ToolDefinition{
		{
			Name:        "retrieve_nutrition_logs",
			Description: "Возвращает последние записи о питании. Используй перед анализом рациона или по запросу пользователя.",
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
	}
}

func (n *nutritionToolExecutor) Execute(ctx context.Context, chatID int64, name string, args json.RawMessage) (string, error) {
	switch name {
	case "retrieve_nutrition_logs":
		return n.retrieveNutritionLogs(ctx, chatID, args)
	default:
		return "", fmt.Errorf("unsupported tool %s", name)
	}
}

func (n *nutritionToolExecutor) retrieveNutritionLogs(ctx context.Context, chatID int64, raw json.RawMessage) (string, error) {
	var params rangeArgs
	if err := decodeToolArgs(raw, &params); err != nil {
		return "", err
	}
	params.applyDefaults(3, 20, 30, 50)
	tz := n.timezone
	if tz == nil {
		tz = time.UTC
	}
	since := time.Now().In(tz).AddDate(0, 0, -params.LookbackDays)
	targets := n.collectTargets(chatID)
	if len(targets) == 0 {
		return "", fmt.Errorf("retrieve_nutrition_logs: chat context is required")
	}
	entries, err := n.loadEntries(ctx, targets, since, params.Limit)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"lookback_days": params.LookbackDays,
		"items":         serializeNutritionEntries(entries),
	}
	return marshalToolPayload(payload)
}

func (n *nutritionToolExecutor) collectTargets(chatID int64) []int64 {
	var targets []int64
	if chatID != 0 {
		targets = append(targets, chatID)
	}
	targets = append(targets, n.logChatIDs...)
	seen := map[int64]struct{}{}
	var deduped []int64
	for _, id := range targets {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		deduped = append(deduped, id)
	}
	return deduped
}

func (n *nutritionToolExecutor) loadEntries(ctx context.Context, chatIDs []int64, since time.Time, limit int) ([]store.NutritionEntry, error) {
	if len(chatIDs) == 0 {
		return nil, nil
	}
	var combined []store.NutritionEntry
	for _, id := range chatIDs {
		data, err := n.store.RecentNutritionEntries(ctx, id, since, limit)
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

func decodeToolArgs(raw json.RawMessage, target interface{}) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, target)
}

func marshalToolPayload(payload any) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func serializeNutritionEntries(entries []store.NutritionEntry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		item := map[string]any{
			"id":            entry.ID,
			"summary":       entry.Summary,
			"original_text": entry.OriginalText,
			"message_ts":    entry.MessageTS.Format(time.RFC3339),
			"chat_id":       entry.ChatID,
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
