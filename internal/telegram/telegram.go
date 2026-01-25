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
	store    *store.Store
	provider llm.Provider
	sender   Sender
	bot      *tgbotapi.BotAPI
	mode     map[int64]string
}

func NewHandler(store *store.Store, provider llm.Provider, sender Sender, bot *tgbotapi.BotAPI) *Handler {
	return &Handler{
		store:    store,
		provider: provider,
		sender:   sender,
		bot:      bot,
		mode:     map[int64]string{},
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
		return h.sender.SendMessage(ctx, chatID, "Welcome to LifeOps! Use /coach /sleep /finance to switch modes.")
	case text == "/whoami":
		return h.sender.SendMessage(ctx, chatID, fmt.Sprintf("chat_id=%d", chatID))
	case text == "/daily":
		return h.sender.SendMessage(ctx, chatID, "Daily digest will be sent by the worker.")
	case text == "/coach", text == "/sleep", text == "/finance":
		h.mode[chatID] = strings.TrimPrefix(text, "/")
		return h.sender.SendMessage(ctx, chatID, fmt.Sprintf("Switched to %s mode.", h.mode[chatID]))
	}

	agent := h.mode[chatID]
	if agent == "" {
		agent = "coach"
	}
	if err := h.store.SaveChatMessage(ctx, chatID, agent, "user", text); err != nil {
		return err
	}
	response, err := h.provider.Chat(ctx, buildChatPrompt(agent, text))
	if err != nil {
		return err
	}
	if err := h.store.SaveChatMessage(ctx, chatID, agent, "assistant", response); err != nil {
		return err
	}
	return h.sender.SendMessage(ctx, chatID, response)
}

func buildChatPrompt(agent string, userMessage string) string {
	switch agent {
	case "sleep":
		return "Ты эксперт по сну и восстановлению. Говори по-русски, дружелюбно и неформально. Вопрос: " + userMessage
	case "finance":
		return "Ты финансовый консультант, но говоришь по-русски и без официоза. Вопрос: " + userMessage
	default:
		return "Ты спортивный тренер. Отвечай на русском, легко и неформально. Вопрос: " + userMessage
	}
}
