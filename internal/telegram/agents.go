package telegram

import "strings"

var defaultAgentPrompts = map[string]string{
	"coach":   "Ты спортивный тренер. Отвечай на русском, легко и неформально.",
	"sleep":   "Ты эксперт по сну и восстановлению. Говори по-русски, дружелюбно и неформально.",
	"finance": "Ты финансовый консультант, но говоришь по-русски и без официоза.",
	"nutrition": "Ты нутриционист. Помогай вести дневник питания и давай мягкие советы без осуждения.",
}

func ResolvePrompt(agent string, prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	if trimmed != "" {
		return trimmed
	}
	if fallback, ok := defaultAgentPrompts[agent]; ok {
		return fallback
	}
	return "Ты дружелюбный ассистент. Отвечай по-русски, легко и по делу."
}
