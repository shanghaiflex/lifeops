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
	PostgresDSN       string
	TelegramToken     string
	TelegramBotTokens map[string]string
	TelegramChatID    int64
	LLMProvider       string
	OpenAIAPIKey      string
	OpenAIModel       string
	Timezone          *time.Location
	BindAddr          string
	FinanceDataDir    string
	ContextLookback   int
	ChatHistoryLimit  int
	TelegramAgentDir  string
	TelegramAgents    map[string]AgentConfig
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
	Name              string
	Prompt            string
	DailyReviewPrompt string
	Timezone          *time.Location
	DailyReviewTime   DailyReviewTime
}

type agentConfigYAML struct {
	Name              string `yaml:"name"`
	Prompt            string `yaml:"prompt"`
	DailyReviewPrompt string `yaml:"daily_review_prompt"`
	Timezone          string `yaml:"timezone"`
	DailyReviewTime   string `yaml:"daily_review_time"`
}

const DefaultDailyReviewPrompt = `Сделай короткий ежедневный обзор для пользователя.
Опирайся на данные ниже и отвечай на русском.
Тренировки и активность: {health_summary}
Метрики здоровья: {metrics_summary}
Финансы: {finance_summary}`

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
		tzName := strings.TrimSpace(raw.Timezone)
		if tzName == "" {
			tzName = "Europe/Moscow"
		}
		loc, err := time.LoadLocation(tzName)
		if err != nil {
			return nil, fmt.Errorf("parse timezone for %s: %w", agentName, err)
		}
		agent.Timezone = loc
		if raw.DailyReviewTime == "" {
			agent.DailyReviewTime = DailyReviewTime{Hour: 9, Minute: 0}
		} else {
			parsed, err := time.Parse("15:04", raw.DailyReviewTime)
			if err != nil {
				return nil, fmt.Errorf("parse daily_review_time for %s: %w", agentName, err)
			}
			agent.DailyReviewTime = DailyReviewTime{Hour: parsed.Hour(), Minute: parsed.Minute()}
		}
		if agent.DailyReviewPrompt == "" {
			agent.DailyReviewPrompt = DefaultDailyReviewPrompt
		}
		out[agentName] = agent
	}
	return out, nil
}
