package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	PostgresDSN     string
	APIKey          string
	TelegramToken   string
	TelegramChatID  int64
	LLMProvider     string
	OpenAIAPIKey    string
	OpenAIModel     string
	Timezone        *time.Location
	BindAddr        string
	FinanceDataDir  string
	ContextLookback int
}

func Load() (*Config, error) {
	cfg := &Config{
		PostgresDSN:    os.Getenv("POSTGRES_DSN"),
		APIKey:         os.Getenv("API_KEY"),
		TelegramToken:  os.Getenv("TELEGRAM_BOT_TOKEN"),
		LLMProvider:    os.Getenv("LLM_PROVIDER"),
		OpenAIAPIKey:   os.Getenv("OPENAI_API_KEY"),
		OpenAIModel:    os.Getenv("OPENAI_MODEL"),
		BindAddr:       envOrDefault("BIND_ADDR", "0.0.0.0:8080"),
		FinanceDataDir: envOrDefault("FINANCE_DATA_DIR", "./data/finance"),
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
	if cfg.PostgresDSN == "" {
		return nil, fmt.Errorf("POSTGRES_DSN is required")
	}
	return cfg, nil
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
