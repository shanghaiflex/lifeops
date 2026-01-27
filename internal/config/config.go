package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	PostgresDSN           string
	TelegramToken         string
	TelegramBotTokens     map[string]string
	TelegramChatID        int64
	LLMProvider           string
	OpenAIAPIKey          string
	OpenAIModel           string
	Timezone              *time.Location
	BindAddr              string
	FinanceDataDir        string
	ContextLookback       int
	ChatHistoryLimit      int
	TelegramAgentDir      string
	TelegramAgents        map[string]AgentConfig
	CalendarSources       []CalendarSource
	CalendarSyncInterval  time.Duration
	CalendarLookbackDays  int
	CalendarLookaheadDays int
}

func Load() (*Config, error) {
	cfg := &Config{
		PostgresDSN:      os.Getenv("POSTGRES_DSN"),
		TelegramToken:    os.Getenv("TELEGRAM_BOT_TOKEN"),
		LLMProvider:      os.Getenv("LLM_PROVIDER"),
		OpenAIAPIKey:     os.Getenv("OPENAI_API_KEY"),
		OpenAIModel:      os.Getenv("OPENAI_MODEL"),
		BindAddr:         envOrDefault("BIND_ADDR", "0.0.0.0:8080"),
		FinanceDataDir:   envOrDefault("FINANCE_DATA_DIR", "./data/finance"),
		TelegramAgentDir: envOrDefault("TELEGRAM_AGENT_CONFIG_DIR", "./data/telegram_agents"),
		ChatHistoryLimit: func() int {
			if v := os.Getenv("CHAT_HISTORY_LIMIT"); v != "" {
				if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
					return parsed
				}
			}
			return 20
		}(),
		ContextLookback: func() int {
			if v := os.Getenv("CONTEXT_LOOKBACK_DAYS"); v != "" {
				if parsed, err := strconv.Atoi(v); err == nil {
					return parsed
				}
			}
			return 30
		}(),
		CalendarLookbackDays: func() int {
			if v := os.Getenv("CALENDAR_LOOKBACK_DAYS"); v != "" {
				if parsed, err := strconv.Atoi(v); err == nil && parsed >= 0 {
					return parsed
				}
			}
			return 3
		}(),
		CalendarLookaheadDays: func() int {
			if v := os.Getenv("CALENDAR_LOOKAHEAD_DAYS"); v != "" {
				if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
					return parsed
				}
			}
			return 14
		}(),
		CalendarSyncInterval: func() time.Duration {
			if v := os.Getenv("CALENDAR_SYNC_INTERVAL"); v != "" {
				if parsed, err := time.ParseDuration(v); err == nil && parsed > 0 {
					return parsed
				}
			}
			return 30 * time.Minute
		}(),
	}
	if cfg.OpenAIModel == "" {
		cfg.OpenAIModel = "gpt-5-mini"
	}
	if cfg.LLMProvider == "" {
		cfg.LLMProvider = "mock"
	}
	if tokens := os.Getenv("TELEGRAM_BOT_TOKENS"); tokens != "" {
		parsed, err := parseKeyValueMap(tokens)
		if err != nil {
			return nil, fmt.Errorf("parse TELEGRAM_BOT_TOKENS: %w", err)
		}
		cfg.TelegramBotTokens = parsed
	}
	if sources := strings.TrimSpace(os.Getenv("CALENDAR_SOURCES")); sources != "" {
		parsed, err := parseKeyValueMap(sources)
		if err != nil {
			return nil, fmt.Errorf("parse CALENDAR_SOURCES: %w", err)
		}
		list := make([]CalendarSource, 0, len(parsed))
		for id, url := range parsed {
			id = strings.TrimSpace(id)
			url = strings.TrimSpace(url)
			if id == "" || url == "" {
				continue
			}
			list = append(list, CalendarSource{ID: id, URL: url})
		}
		sort.Slice(list, func(i, j int) bool {
			return list[i].ID < list[j].ID
		})
		cfg.CalendarSources = list
	}
	if tz := os.Getenv("TIMEZONE"); tz != "" {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return nil, fmt.Errorf("load timezone: %w", err)
		}
		cfg.Timezone = loc
	} else {
		cfg.Timezone = time.UTC
	}
	if chat := os.Getenv("TELEGRAM_CHAT_ID"); chat != "" {
		val, err := strconv.ParseInt(chat, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse TELEGRAM_CHAT_ID: %w", err)
		}
		cfg.TelegramChatID = val
	}
	if cfg.TelegramAgentDir != "" {
		agents, err := readAgentConfigs(cfg.TelegramAgentDir)
		if err != nil {
			return nil, fmt.Errorf("read TELEGRAM_AGENT_CONFIG_DIR: %w", err)
		}
		cfg.TelegramAgents = agents
	}
	if cfg.PostgresDSN == "" {
		return nil, fmt.Errorf("POSTGRES_DSN is required")
	}
	if cfg.TelegramToken == "" && len(cfg.TelegramBotTokens) > 0 {
		if token, ok := cfg.TelegramBotTokens["coach"]; ok {
			cfg.TelegramToken = token
		} else {
			keys := make([]string, 0, len(cfg.TelegramBotTokens))
			for key := range cfg.TelegramBotTokens {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			if len(keys) > 0 {
				cfg.TelegramToken = cfg.TelegramBotTokens[keys[0]]
			}
		}
	}
	return cfg, nil
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func parseKeyValueMap(input string) (map[string]string, error) {
	out := make(map[string]string)
	entries := strings.Split(input, ",")
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return nil, fmt.Errorf("invalid entry %q, expected name=token", entry)
		}
		out[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	return out, nil
}

type DailyReviewTime struct {
	Hour   int
	Minute int
}

type AgentConfig struct {
	Name                         string
	Prompt                       string
	DailyReviewPrompt            string
	Timezone                     *time.Location
	DailyReviewTime              DailyReviewTime
	DailyReviewTimes             []DailyReviewTime
	TelegramToken                string
	DefaultChatIDs               []int64
	NutritionLogChatID           int64
	NutritionReviewChatID        int64
	NutritionLogTelegramToken    string
	NutritionReviewTelegramToken string
	NutritionLogPrompt           string
	NutritionReviewPrompt        string
}

type CalendarSource struct {
	ID  string
	URL string
}

type agentConfigYAML struct {
	Name                         string `yaml:"name"`
	Prompt                       string `yaml:"prompt"`
	DailyReviewPrompt            string `yaml:"daily_review_prompt"`
	Timezone                     string `yaml:"timezone"`
	DailyReviewTime              string `yaml:"daily_review_time"`
	DailyReviewTimes             string `yaml:"daily_review_times"`
	TelegramToken                string `yaml:"telegram_token"`
	ChatID                       string `yaml:"chat_id"`
	NutritionLogChatID           string `yaml:"nutrition_log_chat_id"`
	NutritionReviewChatID        string `yaml:"nutrition_review_chat_id"`
	NutritionLogTelegramToken    string `yaml:"nutrition_log_telegram_token"`
	NutritionReviewTelegramToken string `yaml:"nutrition_review_telegram_token"`
	NutritionLogPrompt           string `yaml:"nutrition_log_prompt"`
	NutritionReviewPrompt        string `yaml:"nutrition_review_prompt"`
}

const DefaultDailyReviewPrompt = `Сделай короткий ежедневный обзор для пользователя.
Перед ответом получи актуальные данные через доступные инструменты и только потом сформулируй рекомендации на русском языке.`

func parseAgentConfigYAML(data []byte) (agentConfigYAML, error) {
	var cfg agentConfigYAML
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var (
		currentKey string
		blockLines []string
	)
	flushBlock := func() {
		if currentKey == "" {
			return
		}
		value := strings.TrimSpace(strings.Join(blockLines, "\n"))
		switch currentKey {
		case "name":
			cfg.Name = value
		case "prompt":
			cfg.Prompt = value
		case "daily_review_prompt":
			cfg.DailyReviewPrompt = value
		case "timezone":
			cfg.Timezone = value
		case "daily_review_time":
			cfg.DailyReviewTime = value
		case "daily_review_times":
			cfg.DailyReviewTimes = value
		case "nutrition_log_chat_id":
			cfg.NutritionLogChatID = value
		case "nutrition_review_chat_id":
			cfg.NutritionReviewChatID = value
		case "nutrition_log_telegram_token":
			cfg.NutritionLogTelegramToken = value
		case "nutrition_review_telegram_token":
			cfg.NutritionReviewTelegramToken = value
		case "nutrition_log_prompt":
			cfg.NutritionLogPrompt = value
		case "nutrition_review_prompt":
			cfg.NutritionReviewPrompt = value
		}
		currentKey = ""
		blockLines = nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if currentKey != "" {
			if trimmed == "" {
				blockLines = append(blockLines, "")
				continue
			}
			if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
				blockLines = append(blockLines, strings.TrimLeft(line, " \t"))
				continue
			}
			flushBlock()
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			return cfg, fmt.Errorf("invalid line: %q", line)
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if value == "|" {
			currentKey = key
			blockLines = nil
			continue
		}
		value = strings.Trim(value, `"'`)
		switch key {
		case "name":
			cfg.Name = value
		case "prompt":
			cfg.Prompt = value
		case "daily_review_prompt":
			cfg.DailyReviewPrompt = value
		case "timezone":
			cfg.Timezone = value
		case "daily_review_time":
			cfg.DailyReviewTime = value
		case "daily_review_times":
			cfg.DailyReviewTimes = value
		case "telegram_token":
			cfg.TelegramToken = value
		case "chat_id":
			cfg.ChatID = value
		case "nutrition_log_chat_id":
			cfg.NutritionLogChatID = value
		case "nutrition_review_chat_id":
			cfg.NutritionReviewChatID = value
		case "nutrition_log_telegram_token":
			cfg.NutritionLogTelegramToken = value
		case "nutrition_review_telegram_token":
			cfg.NutritionReviewTelegramToken = value
		case "nutrition_log_prompt":
			cfg.NutritionLogPrompt = value
		case "nutrition_review_prompt":
			cfg.NutritionReviewPrompt = value
		}
	}
	if err := scanner.Err(); err != nil {
		return cfg, err
	}
	if currentKey != "" {
		flushBlock()
	}
	return cfg, nil
}

func readAgentConfigs(dir string) (map[string]AgentConfig, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make(map[string]AgentConfig)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ext)
		if name == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		raw, err := parseAgentConfigYAML(data)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", entry.Name(), err)
		}
		agentName := strings.TrimSpace(raw.Name)
		if agentName == "" {
			agentName = name
		}
		agentName = strings.TrimSpace(agentName)
		if agentName == "" {
			continue
		}
		agent := AgentConfig{
			Name:              agentName,
			Prompt:            strings.TrimSpace(raw.Prompt),
			DailyReviewPrompt: strings.TrimSpace(raw.DailyReviewPrompt),
		}
		agent.TelegramToken = resolveEnvReference(strings.TrimSpace(raw.TelegramToken))
		agent.NutritionLogTelegramToken = resolveEnvReference(strings.TrimSpace(raw.NutritionLogTelegramToken))
		agent.NutritionReviewTelegramToken = resolveEnvReference(strings.TrimSpace(raw.NutritionReviewTelegramToken))
		agent.NutritionLogPrompt = strings.TrimSpace(raw.NutritionLogPrompt)
		agent.NutritionReviewPrompt = strings.TrimSpace(raw.NutritionReviewPrompt)
		tzName := strings.TrimSpace(raw.Timezone)
		if tzName == "" {
			tzName = "Europe/Moscow"
		}
		loc, err := time.LoadLocation(tzName)
		if err != nil {
			return nil, fmt.Errorf("parse timezone for %s: %w", agentName, err)
		}
		agent.Timezone = loc
		times, err := buildDailyReviewTimes(raw)
		if err != nil {
			return nil, fmt.Errorf("parse daily review times for %s: %w", agentName, err)
		}
		if len(times) == 0 {
			times = []DailyReviewTime{{Hour: 9, Minute: 0}}
		}
		agent.DailyReviewTimes = times
		agent.DailyReviewTime = times[0]
		if agent.DailyReviewPrompt == "" {
			agent.DailyReviewPrompt = DefaultDailyReviewPrompt
		}
		if chatID := resolveEnvReference(strings.TrimSpace(raw.ChatID)); chatID != "" {
			parsedID, err := strconv.ParseInt(chatID, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse chat_id for %s: %w", agentName, err)
			}
			agent.DefaultChatIDs = []int64{parsedID}
		}
		if id, err := parseChatIDValue(raw.NutritionLogChatID); err != nil {
			return nil, fmt.Errorf("parse nutrition_log_chat_id for %s: %w", agentName, err)
		} else if id != 0 {
			agent.NutritionLogChatID = id
		}
		if id, err := parseChatIDValue(raw.NutritionReviewChatID); err != nil {
			return nil, fmt.Errorf("parse nutrition_review_chat_id for %s: %w", agentName, err)
		} else if id != 0 {
			agent.NutritionReviewChatID = id
		}
		if strings.EqualFold(agentName, "nutrition") {
			if agent.NutritionLogChatID == 0 && len(agent.DefaultChatIDs) > 0 {
				agent.NutritionLogChatID = agent.DefaultChatIDs[0]
			}
			if agent.NutritionReviewChatID == 0 {
				switch {
				case len(agent.DefaultChatIDs) > 0:
					agent.NutritionReviewChatID = agent.DefaultChatIDs[0]
				case agent.NutritionLogChatID != 0:
					agent.NutritionReviewChatID = agent.NutritionLogChatID
				}
			}
			if agent.NutritionLogTelegramToken == "" {
				agent.NutritionLogTelegramToken = agent.TelegramToken
			}
			if agent.NutritionReviewTelegramToken == "" {
				agent.NutritionReviewTelegramToken = agent.TelegramToken
			}
			if strings.TrimSpace(agent.NutritionReviewPrompt) != "" {
				agent.Prompt = agent.NutritionReviewPrompt
			}
		}
		out[agentName] = agent
	}
	return out, nil
}

func buildDailyReviewTimes(raw agentConfigYAML) ([]DailyReviewTime, error) {
	if strings.TrimSpace(raw.DailyReviewTimes) != "" {
		return parseDailyReviewTimesList(raw.DailyReviewTimes)
	}
	if strings.TrimSpace(raw.DailyReviewTime) != "" {
		value := resolveEnvReference(strings.TrimSpace(raw.DailyReviewTime))
		parsed, err := time.Parse("15:04", value)
		if err != nil {
			return nil, fmt.Errorf("parse daily_review_time %q: %w", value, err)
		}
		return []DailyReviewTime{{Hour: parsed.Hour(), Minute: parsed.Minute()}}, nil
	}
	return nil, nil
}

func parseDailyReviewTimesList(value string) ([]DailyReviewTime, error) {
	resolved := resolveEnvReference(strings.TrimSpace(value))
	if resolved == "" {
		return nil, nil
	}
	parts := strings.FieldsFunc(resolved, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n'
	})
	var times []DailyReviewTime
	seen := map[string]struct{}{}
	for _, part := range parts {
		slot := strings.TrimSpace(strings.Trim(part, `"'`))
		if slot == "" {
			continue
		}
		parsed, err := time.Parse("15:04", slot)
		if err != nil {
			return nil, fmt.Errorf("parse time %q: %w", slot, err)
		}
		key := fmt.Sprintf("%02d:%02d", parsed.Hour(), parsed.Minute())
		if _, ok := seen[key]; ok {
			continue
		}
		times = append(times, DailyReviewTime{Hour: parsed.Hour(), Minute: parsed.Minute()})
		seen[key] = struct{}{}
	}
	if len(times) == 0 {
		return nil, nil
	}
	sort.Slice(times, func(i, j int) bool {
		if times[i].Hour == times[j].Hour {
			return times[i].Minute < times[j].Minute
		}
		return times[i].Hour < times[j].Hour
	})
	return times, nil
}

func parseChatIDValue(value string) (int64, error) {
	resolved := resolveEnvReference(strings.TrimSpace(value))
	if resolved == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(resolved), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse chat id %q: %w", resolved, err)
	}
	return parsed, nil
}

func resolveEnvReference(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "${") && strings.HasSuffix(trimmed, "}") {
		key := strings.TrimSpace(trimmed[2 : len(trimmed)-1])
		if key == "" {
			return ""
		}
		return os.Getenv(key)
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "env:") {
		key := strings.TrimSpace(trimmed[4:])
		if key == "" {
			return ""
		}
		return os.Getenv(key)
	}
	return trimmed
}
