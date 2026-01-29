package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"lifeops/internal/config"
	"lifeops/internal/llm"
	"lifeops/internal/store"
)

type Sender interface {
	SendMessage(ctx context.Context, chatID int64, text string) error
}

const handlerMaxToolIterations = 5

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
	store             *store.Store
	provider          llm.Provider
	sender            Sender
	bot               *tgbotapi.BotAPI
	mode              map[int64]string
	agent             string
	historyLimit      int
	prompt            string
	agentConfig       *config.AgentConfig
	nutritionFollowUp func(ctx context.Context, chatID int64) error
	isNutritionLogBot bool
	hasDedicatedLog   bool
	tools             toolExecutor
}

type HandlerConfig struct {
	Agent             string
	HistoryLimit      int
	Prompt            string
	AgentConfig       *config.AgentConfig
	NutritionFollowUp func(ctx context.Context, chatID int64) error
	IsNutritionLogBot bool
	HasDedicatedLog   bool
}

func NewHandler(store *store.Store, provider llm.Provider, sender Sender, bot *tgbotapi.BotAPI, cfg HandlerConfig) *Handler {
	historyLimit := cfg.HistoryLimit
	if historyLimit <= 0 {
		historyLimit = 20
	}
	// Create universal tool executor for all agents (not just nutrition)
	var tools toolExecutor
	if cfg.AgentConfig != nil {
		tools = newUniversalToolExecutor(store, cfg.AgentConfig)
	}
	return &Handler{
		store:             store,
		provider:          provider,
		sender:            sender,
		bot:               bot,
		mode:              map[int64]string{},
		agent:             cfg.Agent,
		historyLimit:      historyLimit,
		prompt:            cfg.Prompt,
		agentConfig:       cfg.AgentConfig,
		nutritionFollowUp: cfg.NutritionFollowUp,
		isNutritionLogBot: cfg.IsNutritionLogBot,
		hasDedicatedLog:   cfg.HasDedicatedLog,
		tools:             tools,
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
	isLogChat := h.isNutritionLogChat(chatID)
	switch {
	case text == "/start":
		if isLogChat {
			return h.sender.SendMessage(ctx, chatID, h.nutritionLogPrompt())
		}
		if h.agent != "" {
			return h.sender.SendMessage(ctx, chatID, fmt.Sprintf("Welcome to LifeOps %s bot! Ask me anything.", h.agent))
		}
		return h.sender.SendMessage(ctx, chatID, "Welcome to LifeOps! Use /coach /sleep /finance to switch modes.")
	case text == "/whoami":
		return h.sender.SendMessage(ctx, chatID, fmt.Sprintf("chat_id=%d", chatID))
	case text == "/daily":
		return h.sender.SendMessage(ctx, chatID, "Daily digest will be sent by the worker.")
	case strings.HasPrefix(text, "/remember "):
		return h.handleRememberCommand(ctx, chatID, text)
	case text == "/memories":
		return h.handleMemoriesCommand(ctx, chatID)
	case strings.HasPrefix(text, "/forget "):
		return h.handleForgetCommand(ctx, chatID, text)
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
	if h.shouldLogNutrition(agent, chatID) {
		return h.handleNutritionLog(ctx, agent, msg, text)
	}
	history, err := h.store.ChatHistory(ctx, chatID, agent, h.historyLimit)
	if err != nil {
		return err
	}
	if err := h.store.SaveChatMessage(ctx, chatID, agent, "user", text); err != nil {
		return err
	}
	resp, err := h.generateLLMResponse(ctx, agent, history, text, chatID)
	if err != nil {
		return err
	}
	if err := h.store.SaveChatMessage(ctx, chatID, agent, "assistant", resp); err != nil {
		return err
	}
	return h.sender.SendMessage(ctx, chatID, resp)
}

func (h *Handler) handleRememberCommand(ctx context.Context, chatID int64, text string) error {
	memoryText := strings.TrimSpace(strings.TrimPrefix(text, "/remember"))
	if memoryText == "" {
		return h.sender.SendMessage(ctx, chatID, "Usage: /remember <something to remember>\nExample: /remember I'm training for a marathon in June")
	}
	agent := h.agent
	if agent == "" {
		agent = h.mode[chatID]
	}
	// Save as global memory (agent = empty string) so all agents can see it
	_, err := h.store.SaveUserMemory(ctx, memoryText, "")
	if err != nil {
		return err
	}
	return h.sender.SendMessage(ctx, chatID, fmt.Sprintf("✓ Запомнил: %s", memoryText))
}

func (h *Handler) handleMemoriesCommand(ctx context.Context, chatID int64) error {
	agent := h.agent
	if agent == "" {
		agent = h.mode[chatID]
	}
	if agent == "" {
		agent = "coach"
	}
	memories, err := h.store.GetUserMemories(ctx, agent)
	if err != nil {
		return err
	}
	if len(memories) == 0 {
		return h.sender.SendMessage(ctx, chatID, "У меня пока нет сохранённых воспоминаний. Используй /remember чтобы что-то запомнить.")
	}
	var response strings.Builder
	response.WriteString("Сохранённые воспоминания:\n\n")
	for i, mem := range memories {
		response.WriteString(fmt.Sprintf("%d. %s (ID: %d)\n", i+1, mem.Content, mem.ID))
	}
	response.WriteString("\nИспользуй /forget <ID> чтобы удалить воспоминание")
	return h.sender.SendMessage(ctx, chatID, response.String())
}

func (h *Handler) handleForgetCommand(ctx context.Context, chatID int64, text string) error {
	idStr := strings.TrimSpace(strings.TrimPrefix(text, "/forget"))
	if idStr == "" {
		return h.sender.SendMessage(ctx, chatID, "Usage: /forget <ID>\nПример: /forget 1\n\nИспользуй /memories чтобы посмотреть список с ID")
	}
	var id int64
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		return h.sender.SendMessage(ctx, chatID, "Неверный ID. Используй /memories чтобы посмотреть список.")
	}
	if err := h.store.DeleteUserMemory(ctx, id); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return h.sender.SendMessage(ctx, chatID, "Воспоминание не найдено. Используй /memories чтобы посмотреть список.")
		}
		return err
	}
	return h.sender.SendMessage(ctx, chatID, "✓ Воспоминание удалено")
}

func (h *Handler) generateLLMResponse(ctx context.Context, agent string, history []store.ChatMessage, userMessage string, chatID int64) (string, error) {
	systemPrompt := ResolvePrompt(agent, h.prompt)
	// Load and inject user memories into system prompt
	memories, err := h.store.GetUserMemories(ctx, agent)
	if err != nil {
		log.Printf("failed to load memories: %v", err)
	} else if len(memories) > 0 {
		systemPrompt = injectMemories(systemPrompt, memories)
	}
	messages := buildConversation(systemPrompt, history, userMessage)
	exec := h.toolExecutorForAgent(agent)
	req := llm.ChatRequest{
		Messages: append([]llm.ChatMessage(nil), messages...),
	}
	if exec != nil {
		req.Tools = exec.Definitions()
		req.MaxToolCalls = 4
	}
	content, err := h.runChatWithTools(ctx, req, exec, chatID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("llm returned empty response")
	}
	return strings.TrimSpace(content), nil
}

func (h *Handler) toolExecutorForAgent(agent string) toolExecutor {
	// All agents now have access to all tools
	return h.tools
}

func (h *Handler) runChatWithTools(ctx context.Context, req llm.ChatRequest, exec toolExecutor, chatID int64) (string, error) {
	for i := 0; i < handlerMaxToolIterations; i++ {
		resp, err := h.provider.Chat(ctx, req)
		if err != nil {
			return "", err
		}
		if len(resp.ToolCalls) == 0 {
			return resp.Content, nil
		}
		if exec == nil {
			return "", fmt.Errorf("tool call requested but no executor configured")
		}
		req.Messages = append(req.Messages, llm.ChatMessage{
			Role:      "assistant",
			ToolCalls: resp.ToolCalls,
		})
		for _, call := range resp.ToolCalls {
			output, err := exec.Execute(ctx, chatID, call.Name, call.Arguments)
			if err != nil {
				output = fmt.Sprintf("tool %s error: %v", call.Name, err)
			}
			req.Messages = append(req.Messages, llm.ChatMessage{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    output,
			})
		}
	}
	return "", fmt.Errorf("tool call loop exceeded %d iterations", handlerMaxToolIterations)
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
	isLogChat := h.isNutritionLogChat(chatID)
	storageChatID := h.storageNutritionChatID(chatID)
	if strings.TrimSpace(text) == "" {
		return h.sender.SendMessage(ctx, chatID, "Добавь короткое описание того, что ты ел или пил.")
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
		ChatID:       storageChatID,
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
	if !isLogChat && h.shouldTriggerNutritionReview(agent) {
		reply = strings.TrimSpace(reply + "\nСоберу данные за день и скоро пришлю рекомендации.")
	}
	if err := h.sender.SendMessage(ctx, chatID, reply); err != nil {
		return err
	}
	h.triggerNutritionReview(agent, chatID)
	return nil
}

func injectMemories(systemPrompt string, memories []store.UserMemory) string {
	if len(memories) == 0 {
		return systemPrompt
	}
	var builder strings.Builder
	builder.WriteString(systemPrompt)
	builder.WriteString("\n\n")
	builder.WriteString("Important facts about the user:\n")
	for _, mem := range memories {
		builder.WriteString("- ")
		builder.WriteString(mem.Content)
		builder.WriteString("\n")
	}
	return builder.String()
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

func (h *Handler) shouldTriggerNutritionReview(agent string) bool {
	if !strings.EqualFold(agent, "nutrition") {
		return false
	}
	return h.agentConfig != nil && h.nutritionFollowUp != nil && strings.EqualFold(h.agentConfig.Name, "nutrition")
}

func (h *Handler) isNutritionLogChat(chatID int64) bool {
	if h.isNutritionLogBot {
		return true
	}
	if h.hasDedicatedLog {
		return false
	}
	if h.agentConfig == nil || !strings.EqualFold(h.agentConfig.Name, "nutrition") {
		return false
	}
	if h.agentConfig.NutritionLogChatID == 0 {
		return false
	}
	return h.agentConfig.NutritionLogChatID == chatID
}

func (h *Handler) nutritionLogPrompt() string {
	if h.agentConfig != nil {
		if prompt := strings.TrimSpace(h.agentConfig.NutritionLogPrompt); prompt != "" {
			return prompt
		}
	}
	return "Это дневник питания. Просто опиши, что ты ел или пил, и я всё запишу."
}

func (h *Handler) triggerNutritionReview(agent string, chatID int64) {
	if !h.shouldTriggerNutritionReview(agent) {
		log.Printf("nutrition review not triggered: agent=%s shouldTrigger=%v hasFollowUp=%v",
			agent, strings.EqualFold(agent, "nutrition"), h.nutritionFollowUp != nil)
		return
	}
	targets := h.nutritionReviewTargets(chatID)
	log.Printf("nutrition review triggered for agent=%s logChat=%d targets=%v", agent, chatID, targets)
	for _, target := range targets {
		targetID := target
		go func() {
			runCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			log.Printf("nutrition follow-up starting for chat %d", targetID)
			if err := h.nutritionFollowUp(runCtx, targetID); err != nil {
				log.Printf("nutrition follow-up error for chat %d: %v", targetID, err)
			} else {
				log.Printf("nutrition follow-up completed successfully for chat %d", targetID)
			}
		}()
	}
}

func (h *Handler) shouldLogNutrition(agent string, chatID int64) bool {
	if !strings.EqualFold(agent, "nutrition") {
		return false
	}
	if h.isNutritionLogBot {
		return true
	}
	if h.hasDedicatedLog {
		return false
	}
	if h.agentConfig == nil || h.agentConfig.NutritionLogChatID == 0 {
		return false
	}
	return h.agentConfig.NutritionLogChatID == chatID
}

func (h *Handler) nutritionReviewTargets(fallback int64) []int64 {
	if h.agentConfig == nil || h.agentConfig.NutritionReviewChatID == 0 {
		return []int64{fallback}
	}
	return []int64{h.agentConfig.NutritionReviewChatID}
}

func (h *Handler) storageNutritionChatID(chatID int64) int64 {
	if h.agentConfig != nil && h.agentConfig.NutritionLogChatID != 0 {
		return h.agentConfig.NutritionLogChatID
	}
	return chatID
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
	return builder.String()
}

func formatNutritionNumber(value float64) string {
	v := math.Round(value*10) / 10
	if math.Abs(v-math.Round(v)) < 0.01 {
		return fmt.Sprintf("%.0f", math.Round(v))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", v), "0"), ".")
}
