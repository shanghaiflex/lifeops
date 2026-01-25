package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/responses"
)

type Provider interface {
	GenerateJSON(ctx context.Context, prompt string) (string, error)
	Chat(ctx context.Context, prompt string) (string, error)
}

type OpenAIProvider struct {
	client *openai.Client
	model  string
}

func NewOpenAIProvider(apiKey, model string) *OpenAIProvider {
	client := openai.NewClient(openai.WithAPIKey(apiKey))
	return &OpenAIProvider{client: client, model: model}
}

func (p *OpenAIProvider) GenerateJSON(ctx context.Context, prompt string) (string, error) {
	resp, err := p.client.Responses.New(ctx, responses.NewParams{
		Model: openai.F(p.model),
		Input: openai.F([]responses.InputItemUnionParam{
			responses.InputItemMessageParam{
				Role: openai.F(responses.InputItemMessageRoleUser),
				Content: openai.F([]responses.InputContentPartUnionParam{
					responses.InputContentPartTextParam{Text: openai.F(prompt)},
				}),
			},
		}),
		ResponseFormat: openai.F(responses.ResponseFormatJSONObjectParam{}),
	})
	if err != nil {
		return "", fmt.Errorf("openai generate json: %w", err)
	}
	return resp.OutputText(), nil
}

func (p *OpenAIProvider) Chat(ctx context.Context, prompt string) (string, error) {
	resp, err := p.client.Responses.New(ctx, responses.NewParams{
		Model: openai.F(p.model),
		Input: openai.F([]responses.InputItemUnionParam{
			responses.InputItemMessageParam{
				Role: openai.F(responses.InputItemMessageRoleUser),
				Content: openai.F([]responses.InputContentPartUnionParam{
					responses.InputContentPartTextParam{Text: openai.F(prompt)},
				}),
			},
		}),
	})
	if err != nil {
		return "", fmt.Errorf("openai chat: %w", err)
	}
	return resp.OutputText(), nil
}

type MockProvider struct {
	JSONResponse string
	ChatResponse string
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

func (m *MockProvider) Chat(_ context.Context, _ string) (string, error) {
	if m.ChatResponse != "" {
		return m.ChatResponse, nil
	}
	return "Mock reply", nil
}
