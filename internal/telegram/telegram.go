package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

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
	text := strings.TrimSpace(extractMessageText(msg))
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
	case text == "/coach", text == "/sleep", text == "/finance", text == "/nutrition":
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
	if agent == "nutrition" {
		return h.handleNutritionLog(ctx, agent, msg, text)
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

type nutritionExtraction struct {
	Summary  string   `json:"summary"`
	Calories *float64 `json:"calories"`
	Protein  *float64 `json:"protein_g"`
	Carbs    *float64 `json:"carbs_g"`
	Fat      *float64 `json:"fat_g"`
	Notes    string   `json:"notes"`
}

func (h *Handler) handleNutritionLog(ctx context.Context, agent string, msg *tgbotapi.Message, text string) error {
	chatID := msg.Chat.ID
	if strings.TrimSpace(text) == "" {
		return h.sender.SendMessage(ctx, chatID, "Добавь короткое описание того, что ты ел или пил.")
	}
	if err := h.store.SaveChatMessage(ctx, chatID, agent, "user", text); err != nil {
		return err
	}
	hasPhoto := len(msg.Photo) > 0
	prompt := buildNutritionExtractionPrompt(text, hasPhoto)
	raw, err := h.provider.GenerateJSON(ctx, prompt)
	if err != nil {
		_ = h.sender.SendMessage(ctx, chatID, "Не смог распознать запись. Попробуй описать блюдо подробнее.")
		return nil
	}
	var parsed nutritionExtraction
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		_ = h.sender.SendMessage(ctx, chatID, "Ответ модели пришёл в неожиданном формате. Попробуй переформулировать сообщение.")
		return nil
	}
	if strings.TrimSpace(parsed.Summary) == "" {
		parsed.Summary = text
	}
	entry := store.NutritionEntry{
		ChatID:       chatID,
		MessageTS:    time.Unix(int64(msg.Date), 0).UTC(),
		OriginalText: text,
		Summary:      strings.TrimSpace(parsed.Summary),
		PhotoFileID:  extractPhotoFileID(msg),
		Raw:          json.RawMessage(raw),
	}
	entry.Calories = parsed.Calories
	entry.ProteinGrams = parsed.Protein
	entry.CarbsGrams = parsed.Carbs
	entry.FatGrams = parsed.Fat
	if _, err := h.store.InsertNutritionEntry(ctx, entry); err != nil {
		return err
	}
	reply := buildNutritionAck(parsed)
	if err := h.store.SaveChatMessage(ctx, chatID, agent, "assistant", reply); err != nil {
		return err
	}
	return h.sender.SendMessage(ctx, chatID, reply)
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

func buildNutritionExtractionPrompt(text string, hasPhoto bool) string {
	photoNote := "Фото не приложено."
	if hasPhoto {
		photoNote = "К сообщению приложено фото блюда (ты не можешь его видеть, опирайся на текст)."
	}
	return fmt.Sprintf(`Ты нутриционист и ведёшь дневник питания пользователя. Задача — оценить калорийность и макронутриенты.
Правила:
- Если точных чисел нет, оцени приблизительно.
- Калории указывай в ккал, макросы — в граммах.
- Ответ возвращай строго в формате JSON:
{
  "summary": "краткое описание блюда на русском",
  "calories": number|null,
  "protein_g": number|null,
  "carbs_g": number|null,
  "fat_g": number|null,
  "notes": "дополнительные короткие советы при необходимости"
}
%s
Текст пользователя:
"""%s"""`, photoNote, text)
}

func extractMessageText(msg *tgbotapi.Message) string {
	if msg == nil {
		return ""
	}
	if strings.TrimSpace(msg.Text) != "" {
		return msg.Text
	}
	return msg.Caption
}

func extractPhotoFileID(msg *tgbotapi.Message) string {
	if msg == nil || len(msg.Photo) == 0 {
		return ""
	}
	photo := msg.Photo[len(msg.Photo)-1]
	return photo.FileID
}

func buildNutritionAck(parsed nutritionExtraction) string {
	var builder strings.Builder
	builder.WriteString("Записал приём пищи: ")
	builder.WriteString(strings.TrimSpace(parsed.Summary))
	var parts []string
	if parsed.Calories != nil {
		parts = append(parts, fmt.Sprintf("%s ккал", formatNutritionNumber(*parsed.Calories)))
	}
	if parsed.Protein != nil {
		parts = append(parts, fmt.Sprintf("белки %s г", formatNutritionNumber(*parsed.Protein)))
	}
	if parsed.Carbs != nil {
		parts = append(parts, fmt.Sprintf("углеводы %s г", formatNutritionNumber(*parsed.Carbs)))
	}
	if parsed.Fat != nil {
		parts = append(parts, fmt.Sprintf("жиры %s г", formatNutritionNumber(*parsed.Fat)))
	}
	if len(parts) > 0 {
		builder.WriteString("\n")
		builder.WriteString(strings.Join(parts, ", "))
	}
	if strings.TrimSpace(parsed.Notes) != "" {
		builder.WriteString("\n")
		builder.WriteString(strings.TrimSpace(parsed.Notes))
	}
	return builder.String()
}

func formatNutritionNumber(value float64) string {
	v := math.Round(value*10) / 10
	if math.Abs(v-math.Round(v)) < 0.01 {
		return fmt.Sprintf("%.0f", math.Round(v))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", v), "0"), ".")
}
