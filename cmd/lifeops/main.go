package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"lifeops/internal/api"
	"lifeops/internal/config"
	"lifeops/internal/finance"
	"lifeops/internal/health"
	"lifeops/internal/llm"
	"lifeops/internal/store"
	"lifeops/internal/telegram"
	"lifeops/internal/worker"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatalf("usage: %s [api|bot|worker|worker-once|ingest-finance]", os.Args[0])
	}
	mode := os.Args[1]

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := store.New(ctx, cfg.PostgresDSN)
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
	case "ingest-finance":
		runIngest(ctx, cfg, store)
	default:
		log.Fatalf("unknown mode %s", mode)
	}
}

func buildProvider(cfg *config.Config) llm.Provider {
	switch cfg.LLMProvider {
	case "openai":
		return llm.NewOpenAIProvider(cfg.OpenAIAPIKey, cfg.OpenAIModel)
	default:
		return &llm.MockProvider{}
	}
}

func runAPI(ctx context.Context, cfg *config.Config, store *store.Store) {
	healthSvc := health.NewService(store)
	server, err := api.NewServer(healthSvc)
	if err != nil {
		log.Fatalf("api server: %v", err)
	}

	httpServer := &http.Server{
		Addr:         cfg.BindAddr,
		Handler:      server.Router(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
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

func runBot(ctx context.Context, cfg *config.Config, store *store.Store) {
	provider := buildProvider(cfg)
	if len(cfg.TelegramAgents) > 0 && len(cfg.TelegramBotTokens) > 0 {
		errCh := make(chan error, len(cfg.TelegramBotTokens))
		started := 0
		for agent, token := range cfg.TelegramBotTokens {
			agent := agent
			token := token
			agentCfg, ok := cfg.TelegramAgents[agent]
			if !ok {
				log.Printf("telegram bot token provided for %s but no agent config found", agent)
				continue
			}
			started++
			go func() {
				bot, err := tgbotapi.NewBotAPI(token)
				if err != nil {
					errCh <- fmt.Errorf("telegram %s: %w", agent, err)
					return
				}
				sender := telegram.NewBotSenderFromBot(bot)
				handler := telegram.NewHandler(store, provider, sender, bot, telegram.HandlerConfig{
					Agent:        agent,
					HistoryLimit: cfg.ChatHistoryLimit,
					Prompt:       agentCfg.Prompt,
				})
				if err := handler.Run(ctx); err != nil && ctx.Err() == nil {
					errCh <- fmt.Errorf("bot %s: %w", agent, err)
				}
			}()
		}
		if started == 0 {
			log.Printf("telegram bot tokens provided but no matching agent configs; skipping bot startup")
			return
		}
		select {
		case <-ctx.Done():
			return
		case err := <-errCh:
			log.Fatalf("bot: %v", err)
		}
		return
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

func runWorker(ctx context.Context, cfg *config.Config, store *store.Store, loop bool) {
	provider := buildProvider(cfg)
	if len(cfg.TelegramAgents) > 0 && len(cfg.TelegramBotTokens) > 0 {
		errCh := make(chan error, len(cfg.TelegramAgents))
		started := 0
		for name, agent := range cfg.TelegramAgents {
			token, ok := cfg.TelegramBotTokens[name]
			if !ok {
				log.Printf("telegram bot token missing for agent %s; skipping daily review", name)
				continue
			}
			sender, err := telegram.NewBotSender(token)
			if err != nil {
				log.Fatalf("telegram sender: %v", err)
			}
			w := worker.New(store, provider, sender, agent, cfg.ChatHistoryLimit, nil)
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
			return
		}
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

func runIngest(ctx context.Context, cfg *config.Config, store *store.Store) {
	ingestor := finance.NewIngestor(store)
	count, err := ingestor.IngestDirectory(ctx, cfg.FinanceDataDir)
	if err != nil {
		log.Fatalf("ingest finance: %v", err)
	}
	fmt.Printf("Imported %d transactions\n", count)
}
