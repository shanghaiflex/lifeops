package worker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"lifeops/internal/llm"
)

type WorkerObserver interface {
	OnLLMRequest(agent string, messages []llm.ChatMessage)
	OnLLMResponse(agent string, response llm.ChatResponse)
	OnToolResult(agent string, call llm.ToolCall, output string)
}

type DebugObserver struct {
	writer io.Writer
	mu     sync.Mutex
}

func NewDebugObserver(writer io.Writer) *DebugObserver {
	if writer == nil {
		writer = io.Discard
	}
	return &DebugObserver{writer: writer}
}

func (o *DebugObserver) OnLLMRequest(agent string, messages []llm.ChatMessage) {
	o.mu.Lock()
	defer o.mu.Unlock()
	fmt.Fprintf(o.writer, "=== LLM request (%s) ===\n", agent)
	for idx, msg := range messages {
		fmt.Fprintf(o.writer, "%d. [%s]\n", idx+1, strings.ToUpper(msg.Role))
		if msg.ToolCallID != "" {
			fmt.Fprintf(o.writer, "tool_call_id=%s\n", msg.ToolCallID)
		}
		if strings.TrimSpace(msg.Content) != "" {
			fmt.Fprintf(o.writer, "%s\n", msg.Content)
		}
		if len(msg.ToolCalls) > 0 {
			for _, call := range msg.ToolCalls {
				fmt.Fprintf(o.writer, "requested tool=%s args=%s\n", call.Name, prettyJSON(call.Arguments))
			}
		}
	}
	fmt.Fprintln(o.writer)
}

func (o *DebugObserver) OnLLMResponse(agent string, response llm.ChatResponse) {
	o.mu.Lock()
	defer o.mu.Unlock()
	fmt.Fprintf(o.writer, "=== LLM response (%s) ===\n", agent)
	if strings.TrimSpace(response.Content) != "" {
		fmt.Fprintf(o.writer, "%s\n", strings.TrimSpace(response.Content))
	}
	if len(response.ToolCalls) > 0 {
		fmt.Fprintln(o.writer, "tool calls:")
		for _, call := range response.ToolCalls {
			fmt.Fprintf(o.writer, "- id=%s name=%s args=%s\n", call.ID, call.Name, prettyJSON(call.Arguments))
		}
	}
	fmt.Fprintln(o.writer)
}

func (o *DebugObserver) OnToolResult(agent string, call llm.ToolCall, output string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	fmt.Fprintf(o.writer, "=== Tool result (%s) %s ===\n", agent, call.Name)
	fmt.Fprintf(o.writer, "arguments: %s\n", prettyJSON(call.Arguments))
	fmt.Fprintf(o.writer, "output: %s\n\n", prettyJSONString(output))
}

func prettyJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	return prettyJSONString(string(raw))
}

func prettyJSONString(value string) string {
	if strings.TrimSpace(value) == "" {
		return value
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(value), "", "  "); err == nil {
		return buf.String()
	}
	return value
}
