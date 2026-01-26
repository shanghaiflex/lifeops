package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"lifeops/internal/api"
	"lifeops/internal/config"
	"lifeops/internal/finance"
	"lifeops/internal/health"
	"lifeops/internal/llm"
	storepkg "lifeops/internal/store"
	"lifeops/internal/telegram"
	"lifeops/internal/worker"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatalf("usage: %s [api|bot|worker|worker-once|worker-debug|ingest-finance|seed-sample]", os.Args[0])
	}
	mode := os.Args[1]

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := storepkg.New(ctx, cfg.PostgresDSN)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer store.Close()

	switch mode {
	case "api":
		runAPI(ctx, cfg, store)
	case "bot":
		runBot(ctx, cfg, store)
	case "worker":
		runWorker(ctx, cfg, store, true)
	case "worker-once":
		runWorker(ctx, cfg, store, false)
	case "worker-debug":
		agentFilter := ""
		if len(os.Args) > 2 {
			agentFilter = os.Args[2]
		}
		runWorkerDebug(ctx, cfg, store, agentFilter)
	case "ingest-finance":
		runIngest(ctx, cfg, store)
	case "seed-sample":
		runSeedSample(ctx, cfg, store)
	default:
		log.Fatalf("unknown mode %s", mode)
	}
}

func buildProvider(cfg *config.Config) llm.Provider {
	switch strings.ToLower(cfg.LLMProvider) {
	case "openai", "openapi":
		return llm.NewOpenAIProvider(cfg.OpenAIAPIKey, cfg.OpenAIModel)
	default:
		return &llm.MockProvider{}
	}
}

func runAPI(ctx context.Context, cfg *config.Config, store *storepkg.Store) {
	healthSvc := health.NewService(store)
	provider := buildProvider(cfg)
	server, err := api.NewServer(healthSvc, store, cfg, provider)
	if err != nil {
		log.Fatalf("api server: %v", err)
	}

	httpServer := &http.Server{
		Addr:         cfg.BindAddr,
		Handler:      server.Router(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 2 * time.Minute,
		IdleTimeout:  2 * time.Minute,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	log.Printf("API listening on %s", cfg.BindAddr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen: %v", err)
	}
}

func runBot(ctx context.Context, cfg *config.Config, store *storepkg.Store) {
	provider := buildProvider(cfg)
	if len(cfg.TelegramAgents) > 0 {
		errCh := make(chan error, len(cfg.TelegramAgents))
		started := 0
		for agentName, agentCfg := range cfg.TelegramAgents {
			agentCfg := agentCfg
			token := agentCfg.TelegramToken
			if token == "" && len(cfg.TelegramBotTokens) > 0 {
				token = cfg.TelegramBotTokens[agentName]
			}
			if token == "" {
				log.Printf("telegram bot token missing for agent %s; skipping bot startup", agentName)
				continue
			}
			started++
			go func(token string, agentCfg config.AgentConfig) {
				bot, err := tgbotapi.NewBotAPI(token)
				if err != nil {
					errCh <- fmt.Errorf("telegram %s: %w", agentCfg.Name, err)
					return
				}
				sender := telegram.NewBotSenderFromBot(bot)
				handler := telegram.NewHandler(store, provider, sender, bot, telegram.HandlerConfig{
					Agent:        agentCfg.Name,
					HistoryLimit: cfg.ChatHistoryLimit,
					Prompt:       agentCfg.Prompt,
				})
				if err := handler.Run(ctx); err != nil && ctx.Err() == nil {
					errCh <- fmt.Errorf("bot %s: %w", agentCfg.Name, err)
				}
			}(token, agentCfg)
		}
		if started > 0 {
			select {
			case <-ctx.Done():
				return
			case err := <-errCh:
				log.Fatalf("bot: %v", err)
			}
			return
		}
		log.Printf("telegram agent configs present but no valid tokens found; falling back to default bot")
	}
	bot, err := tgbotapi.NewBotAPI(cfg.TelegramToken)
	if err != nil {
		log.Fatalf("telegram: %v", err)
	}
	sender := telegram.NewBotSenderFromBot(bot)
	handler := telegram.NewHandler(store, provider, sender, bot, telegram.HandlerConfig{
		HistoryLimit: cfg.ChatHistoryLimit,
	})
	if err := handler.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("bot: %v", err)
	}
}

func runWorker(ctx context.Context, cfg *config.Config, store *storepkg.Store, loop bool) {
	provider := buildProvider(cfg)
	if len(cfg.TelegramAgents) > 0 {
		errCh := make(chan error, len(cfg.TelegramAgents))
		started := 0
		for name, agent := range cfg.TelegramAgents {
			agent := agent
			token := agent.TelegramToken
			if token == "" && len(cfg.TelegramBotTokens) > 0 {
				token = cfg.TelegramBotTokens[name]
			}
			if token == "" {
				log.Printf("telegram bot token missing for agent %s; skipping daily review", name)
				continue
			}
			sender, err := telegram.NewBotSender(token)
			if err != nil {
				log.Fatalf("telegram sender: %v", err)
			}
			w := worker.New(store, provider, sender, agent, cfg.ChatHistoryLimit, agent.DefaultChatIDs)
			started++
			go func() {
				if loop {
					if err := w.RunDaily(ctx); err != nil && ctx.Err() == nil {
						errCh <- err
					}
					return
				}
				errCh <- w.RunOnce(ctx)
			}()
		}
		if started == 0 {
			log.Printf("no agent workers started for daily review")
		} else {
			if loop {
				select {
				case <-ctx.Done():
					return
				case err := <-errCh:
					log.Fatalf("worker: %v", err)
				}
			} else {
				for i := 0; i < started; i++ {
					if err := <-errCh; err != nil {
						log.Fatalf("worker once: %v", err)
					}
				}
			}
			return
		}
	}
	var sender telegram.Sender = &telegram.NoopSender{}
	if cfg.TelegramToken != "" {
		realSender, err := telegram.NewBotSender(cfg.TelegramToken)
		if err != nil {
			log.Fatalf("telegram sender: %v", err)
		}
		sender = realSender
	}
	agent := config.AgentConfig{
		Name:              "coach",
		Prompt:            "",
		DailyReviewPrompt: config.DefaultDailyReviewPrompt,
		Timezone:          cfg.Timezone,
		DailyReviewTime:   config.DailyReviewTime{Hour: 9, Minute: 0},
	}
	defaultChatIDs := []int64{}
	if cfg.TelegramChatID != 0 {
		defaultChatIDs = []int64{cfg.TelegramChatID}
	}
	w := worker.New(store, provider, sender, agent, cfg.ChatHistoryLimit, defaultChatIDs)
	if loop {
		if err := w.RunDaily(ctx); err != nil && ctx.Err() == nil {
			log.Fatalf("worker: %v", err)
		}
		return
	}
	if err := w.RunOnce(ctx); err != nil {
		log.Fatalf("worker once: %v", err)
	}
}

func runWorkerDebug(ctx context.Context, cfg *config.Config, store *storepkg.Store, agentFilter string) {
	provider := buildProvider(cfg)
	observer := worker.NewDebugObserver(os.Stdout)
	runForAgent := func(agent config.AgentConfig) {
		var sender telegram.Sender = &telegram.NoopSender{}
		w := worker.New(store, provider, sender, agent, cfg.ChatHistoryLimit, agent.DefaultChatIDs)
		w.SetObserver(observer)
		if err := w.RunOnce(ctx); err != nil {
			log.Fatalf("worker debug (%s): %v", agent.Name, err)
		}
	}
	if len(cfg.TelegramAgents) > 0 {
		if agentFilter != "" {
			agent, ok := cfg.TelegramAgents[agentFilter]
			if !ok {
				log.Fatalf("worker debug: unknown agent %s", agentFilter)
			}
			runForAgent(agent)
			return
		}
		for name, agent := range cfg.TelegramAgents {
			log.Printf("worker debug: running agent %s", name)
			runForAgent(agent)
		}
		return
	}
	agent := config.AgentConfig{
		Name:              "coach",
		Prompt:            "",
		DailyReviewPrompt: config.DefaultDailyReviewPrompt,
		Timezone:          cfg.Timezone,
		DailyReviewTime:   config.DailyReviewTime{Hour: 9, Minute: 0},
	}
	if cfg.TelegramChatID != 0 {
		agent.DefaultChatIDs = []int64{cfg.TelegramChatID}
	}
	runForAgent(agent)
}

func runIngest(ctx context.Context, cfg *config.Config, store *storepkg.Store) {
	ingestor := finance.NewIngestor(store)
	count, err := ingestor.IngestDirectory(ctx, cfg.FinanceDataDir)
	if err != nil {
		log.Fatalf("ingest finance: %v", err)
	}
	fmt.Printf("Imported %d transactions\n", count)
}

func runSeedSample(ctx context.Context, cfg *config.Config, store *storepkg.Store) {
	if err := seedHealthData(ctx, store); err != nil {
		log.Fatalf("seed health: %v", err)
	}
	if err := seedFinanceData(ctx, store); err != nil {
		log.Fatalf("seed finance: %v", err)
	}
	log.Printf("Seeded demo data into %s", cfg.PostgresDSN)
}

func seedHealthData(ctx context.Context, store *storepkg.Store) error {
	now := time.Now().UTC()
	workouts := []storepkg.Workout{
		{
			ID:              "seed-workout-run",
			WorkoutType:     "run",
			Start:           now.AddDate(0, 0, -2).Add(-30 * time.Minute),
			End:             now.AddDate(0, 0, -2).Add(30 * time.Minute),
			DurationMinutes: 60,
			DistanceMeters: func() *float64 {
				v := 8500.0
				return &v
			}(),
			Calories: func() *float64 {
				v := 720.0
				return &v
			}(),
			AverageHeartRate: func() *float64 {
				v := 152.0
				return &v
			}(),
			Raw: mustJSON(map[string]any{
				"type": "run",
			}),
		},
		{
			ID:              "seed-workout-swim",
			WorkoutType:     "swim",
			Start:           now.AddDate(0, 0, -1).Add(-45 * time.Minute),
			End:             now.AddDate(0, 0, -1).Add(15 * time.Minute),
			DurationMinutes: 60,
			Calories: func() *float64 {
				v := 500.0
				return &v
			}(),
			Raw: mustJSON(map[string]any{
				"type": "swim",
			}),
		},
	}
	if _, err := store.UpsertWorkouts(ctx, workouts); err != nil {
		return err
	}
	sleeps := []storepkg.Sleep{
		{
			ID:           "seed-sleep-1",
			Start:        time.Date(now.Year(), now.Month(), now.Day()-1, 23, 0, 0, 0, time.UTC),
			End:          time.Date(now.Year(), now.Month(), now.Day(), 7, 0, 0, 0, time.UTC),
			TotalMinutes: 480,
			RemMinutes:   90,
			DeepMinutes:  70,
			CoreMinutes:  270,
			AwakeMinutes: 50,
			Raw: mustJSON(map[string]any{
				"quality": "good",
			}),
		},
	}
	if _, err := store.UpsertSleep(ctx, sleeps); err != nil {
		return err
	}
	metrics := []storepkg.Metric{
		{
			ID:    "seed-metric-hrv",
			Kind:  "HRV",
			Start: now.AddDate(0, 0, -1),
			End:   now.AddDate(0, 0, -1).Add(time.Hour),
			Value: 85,
			Unit:  "ms",
			Raw:   mustJSON(map[string]any{"value": 85}),
		},
		{
			ID:    "seed-metric-resting_hr",
			Kind:  "resting_hr",
			Start: now.AddDate(0, 0, -1),
			End:   now.AddDate(0, 0, -1).Add(time.Hour),
			Value: 52,
			Unit:  "bpm",
			Raw:   mustJSON(map[string]any{"value": 52}),
		},
	}
	_, err := store.UpsertMetrics(ctx, metrics)
	return err
}

func seedFinanceData(ctx context.Context, store *storepkg.Store) error {
	today := time.Now().UTC()
	raws := []storepkg.FinanceRaw{
		{
			SourceFile: "seed.csv",
			RowNum:     1,
			Payload:    mustJSON(map[string]any{"description": "Cafe"}),
		},
		{
			SourceFile: "seed.csv",
			RowNum:     2,
			Payload:    mustJSON(map[string]any{"description": "Gym"}),
		},
		{
			SourceFile: "seed.csv",
			RowNum:     3,
			Payload:    mustJSON(map[string]any{"description": "Salary"}),
		},
	}
	rawIDs, err := store.InsertFinanceRaw(ctx, raws)
	if err != nil {
		return err
	}
	txs := []storepkg.FinanceTransaction{
		{
			Date:        today.AddDate(0, 0, -3),
			Amount:      -25.5,
			Currency:    "EUR",
			Description: "Cafe breakfast",
			Category:    "food",
			Merchant:    "Daily Cafe",
			RawID:       rawIDs[0],
		},
		{
			Date:        today.AddDate(0, 0, -2),
			Amount:      -60,
			Currency:    "EUR",
			Description: "Gym membership",
			Category:    "fitness",
			Merchant:    "Gym Club",
			IsSub:       true,
			RawID:       rawIDs[1],
		},
		{
			Date:        today.AddDate(0, 0, -5),
			Amount:      2500,
			Currency:    "EUR",
			Description: "Salary",
			Category:    "income",
			Merchant:    "Acme Corp",
			RawID:       rawIDs[2],
		},
	}
	if err := store.InsertFinanceTransactions(ctx, txs); err != nil {
		return err
	}
	return store.RebuildFinanceAggregates(ctx)
}

func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
