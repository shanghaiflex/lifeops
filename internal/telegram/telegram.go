package telegram

import (
	"context"
	"fmt"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"lifeops/internal/llm"
	"lifeops/internal/store"
)

type Sender interface {
	SendMessage(ctx context.Context, chatID int64, text string) error
}

type NoopSender struct{}

func (n *NoopSender) SendMessage(_ context.Context, _ int64, _ string) error {
	return nil
}

type BotSender struct {
	bot *tgbotapi.BotAPI
}

func NewBotSender(token string) (*BotSender, error) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("telegram auth: %w", err)
	}
	return &BotSender{bot: bot}, nil
}

func NewBotSenderFromBot(bot *tgbotapi.BotAPI) *BotSender {
	return &BotSender{bot: bot}
}

func (b *BotSender) SendMessage(ctx context.Context, chatID int64, text string) error {
	msg := tgbotapi.NewMessage(chatID, text)
	_, err := b.bot.Send(msg)
	return err
}

type Handler struct {
	store        *store.Store
	provider     llm.Provider
	sender       Sender
	bot          *tgbotapi.BotAPI
	mode         map[int64]string
	agent        string
	historyLimit int
	prompt       string
}

type HandlerConfig struct {
	Agent        string
	HistoryLimit int
	Prompt       string
}

func NewHandler(store *store.Store, provider llm.Provider, sender Sender, bot *tgbotapi.BotAPI, cfg HandlerConfig) *Handler {
	historyLimit := cfg.HistoryLimit
	if historyLimit <= 0 {
		historyLimit = 20
	}
	return &Handler{
		store:        store,
		provider:     provider,
		sender:       sender,
		bot:          bot,
		mode:         map[int64]string{},
		agent:        cfg.Agent,
		historyLimit: historyLimit,
		prompt:       cfg.Prompt,
	}
}

func (h *Handler) Run(ctx context.Context) error {
	updateConfig := tgbotapi.NewUpdate(0)
	updateConfig.Timeout = 60
	updates := h.bot.GetUpdatesChan(updateConfig)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case update := <-updates:
			if update.Message == nil {
				continue
			}
			if err := h.handleMessage(ctx, update.Message); err != nil {
				continue
			}
		}
	}
}

func (h *Handler) handleMessage(ctx context.Context, msg *tgbotapi.Message) error {
	text := strings.TrimSpace(msg.Text)
	chatID := msg.Chat.ID
	switch {
	case text == "/start":
		if h.agent != "" {
			return h.sender.SendMessage(ctx, chatID, fmt.Sprintf("Welcome to LifeOps %s bot! Ask me anything.", h.agent))
		}
		return h.sender.SendMessage(ctx, chatID, "Welcome to LifeOps! Use /coach /sleep /finance to switch modes.")
	case text == "/whoami":
		return h.sender.SendMessage(ctx, chatID, fmt.Sprintf("chat_id=%d", chatID))
	case text == "/daily":
		return h.sender.SendMessage(ctx, chatID, "Daily digest will be sent by the worker.")
	case text == "/coach", text == "/sleep", text == "/finance":
		if h.agent != "" {
			return h.sender.SendMessage(ctx, chatID, fmt.Sprintf("This bot is for %s. Use that bot for chat.", h.agent))
		}
		h.mode[chatID] = strings.TrimPrefix(text, "/")
		return h.sender.SendMessage(ctx, chatID, fmt.Sprintf("Switched to %s mode.", h.mode[chatID]))
	}

	agent := h.agent
	if agent == "" {
		agent = h.mode[chatID]
	}
	if agent == "" {
		agent = "coach"
	}
	history, err := h.store.ChatHistory(ctx, chatID, agent, h.historyLimit)
	if err != nil {
		return err
	}
	if err := h.store.SaveChatMessage(ctx, chatID, agent, "user", text); err != nil {
		return err
	}
	systemPrompt := ResolvePrompt(agent, h.prompt)
	messages := buildConversation(systemPrompt, history, text)
	resp, err := h.provider.Chat(ctx, llm.ChatRequest{
		Messages: messages,
	})
	if err != nil {
		return err
	}
	if resp.Content == "" {
		return fmt.Errorf("llm returned empty response")
	}
	if err := h.store.SaveChatMessage(ctx, chatID, agent, "assistant", resp.Content); err != nil {
		return err
	}
	return h.sender.SendMessage(ctx, chatID, resp.Content)
}

func buildConversation(systemPrompt string, history []store.ChatMessage, userMessage string) []llm.ChatMessage {
	var messages []llm.ChatMessage
	if strings.TrimSpace(systemPrompt) != "" {
		messages = append(messages, llm.ChatMessage{
			Role:    "system",
			Content: systemPrompt,
		})
	}
	for i := len(history) - 1; i >= 0; i-- {
		role := strings.ToLower(history[i].Role)
		if role == "" {
			continue
		}
		messages = append(messages, llm.ChatMessage{
			Role:    role,
			Content: history[i].Content,
		})
	}
	messages = append(messages, llm.ChatMessage{
		Role:    "user",
		Content: userMessage,
	})
	return messages
}
