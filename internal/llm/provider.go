package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
)

type Provider interface {
	GenerateJSON(ctx context.Context, prompt string) (string, error)
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}

type ChatMessage struct {
	Role       string
	Content    string
	ToolCallID string
	ToolCalls  []ToolCall
}

type ToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]any
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

type ChatRequest struct {
	Messages     []ChatMessage
	Tools        []ToolDefinition
	MaxToolCalls int
	Temperature  float64
}

type ChatResponse struct {
	Content   string
	ToolCalls []ToolCall
}

type OpenAIProvider struct {
	client openai.Client
	model  string
}

func NewOpenAIProvider(apiKey, model string) *OpenAIProvider {
	client := openai.NewClient(option.WithAPIKey(apiKey))
	return &OpenAIProvider{client: client, model: model}
}

func (p *OpenAIProvider) GenerateJSON(ctx context.Context, prompt string) (string, error) {
	resp, err := p.client.Responses.New(ctx, responses.ResponseNewParams{
		Model: shared.ResponsesModel(p.model),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: responses.ResponseInputParam{
				responses.ResponseInputItemUnionParam{
					OfMessage: &responses.EasyInputMessageParam{
						Role: responses.EasyInputMessageRoleUser,
						Content: responses.EasyInputMessageContentUnionParam{
							OfString: openai.String(prompt),
						},
					},
				},
			},
		},
		Text: responses.ResponseTextConfigParam{
			Format: responses.ResponseFormatTextConfigUnionParam{
				OfJSONObject: &shared.ResponseFormatJSONObjectParam{},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("openai generate json: %w", err)
	}
	return resp.OutputText(), nil
}

func (p *OpenAIProvider) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	if len(req.Messages) == 0 {
		return ChatResponse{}, fmt.Errorf("chat: no messages provided")
	}
	inputItems, err := convertMessages(req.Messages)
	if err != nil {
		return ChatResponse{}, err
	}
	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(p.model),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: responses.ResponseInputParam(inputItems),
		},
	}
	if len(req.Tools) > 0 {
		params.Tools = buildToolParams(req.Tools)
		params.ParallelToolCalls = param.NewOpt(false)
	}
	if req.MaxToolCalls > 0 {
		params.MaxToolCalls = param.NewOpt(int64(req.MaxToolCalls))
	}
	if req.Temperature > 0 {
		params.Temperature = param.NewOpt(req.Temperature)
	}
	resp, err := p.client.Responses.New(ctx, params)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("openai chat: %w", err)
	}
	return buildChatResponse(resp), nil
}

func buildToolParams(defs []ToolDefinition) []responses.ToolUnionParam {
	out := make([]responses.ToolUnionParam, 0, len(defs))
	for _, def := range defs {
		tool := &responses.FunctionToolParam{
			Name:       def.Name,
			Parameters: def.Parameters,
			Strict:     param.NewOpt(true),
		}
		if strings.TrimSpace(def.Description) != "" {
			tool.Description = param.NewOpt(def.Description)
		}
		out = append(out, responses.ToolUnionParam{OfFunction: tool})
	}
	return out
}

func buildChatResponse(resp *responses.Response) ChatResponse {
	var (
		builder   strings.Builder
		toolCalls []ToolCall
	)
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			msg := item.AsMessage()
			builder.WriteString(extractMessageText(msg))
		case "function_call":
			call := item.AsFunctionCall()
			toolCalls = append(toolCalls, ToolCall{
				ID:        call.CallID,
				Name:      call.Name,
				Arguments: json.RawMessage(call.Arguments),
			})
		}
	}
	return ChatResponse{
		Content:   strings.TrimSpace(builder.String()),
		ToolCalls: toolCalls,
	}
}

func extractMessageText(msg responses.ResponseOutputMessage) string {
	var builder strings.Builder
	for _, part := range msg.Content {
		switch part.Type {
		case "output_text":
			text := part.AsOutputText()
			builder.WriteString(text.Text)
		case "output_refusal":
			refusal := part.AsRefusal()
			builder.WriteString(refusal.Refusal)
		}
	}
	if builder.Len() > 0 {
		builder.WriteString("\n")
	}
	return builder.String()
}

func convertMessages(messages []ChatMessage) ([]responses.ResponseInputItemUnionParam, error) {
	items := make([]responses.ResponseInputItemUnionParam, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case "system":
			items = append(items, responses.ResponseInputItemParamOfMessage(msg.Content, responses.EasyInputMessageRoleSystem))
		case "user":
			items = append(items, responses.ResponseInputItemParamOfMessage(msg.Content, responses.EasyInputMessageRoleUser))
		case "assistant":
			if strings.TrimSpace(msg.Content) != "" {
				items = append(items, responses.ResponseInputItemParamOfMessage(msg.Content, responses.EasyInputMessageRoleAssistant))
			}
			for _, call := range msg.ToolCalls {
				items = append(items, responses.ResponseInputItemParamOfFunctionCall(string(call.Arguments), call.ID, call.Name))
			}
		case "tool":
			if msg.ToolCallID == "" {
				return nil, fmt.Errorf("tool message missing call_id")
			}
			items = append(items, responses.ResponseInputItemParamOfFunctionCallOutput(msg.ToolCallID, msg.Content))
		default:
			return nil, fmt.Errorf("unsupported message role %q", msg.Role)
		}
	}
	return items, nil
}

type MockChatResponse struct {
	Content   string
	ToolCalls []ToolCall
}

type MockProvider struct {
	JSONResponse string
	ChatResponse string
	ChatRequests []ChatRequest
	Responses    []MockChatResponse
}

func (m *MockProvider) GenerateJSON(_ context.Context, _ string) (string, error) {
	if m.JSONResponse != "" {
		return m.JSONResponse, nil
	}
	payload := map[string]string{
		"coach":   "Mock coach summary",
		"sleep":   "Mock sleep summary",
		"finance": "Mock finance summary",
	}
	data, _ := json.Marshal(payload)
	return string(data), nil
}

func (m *MockProvider) Chat(_ context.Context, req ChatRequest) (ChatResponse, error) {
	m.ChatRequests = append(m.ChatRequests, req)
	if len(m.Responses) > 0 {
		resp := m.Responses[0]
		m.Responses = m.Responses[1:]
		return ChatResponse{
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		}, nil
	}
	if m.ChatResponse != "" {
		return ChatResponse{Content: m.ChatResponse}, nil
	}
	return ChatResponse{Content: "Mock reply"}, nil
}
