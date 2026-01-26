package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
)

type Provider interface {
	GenerateJSON(ctx context.Context, prompt string) (string, error)
	Chat(ctx context.Context, prompt string) (string, error)
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

func (p *OpenAIProvider) Chat(ctx context.Context, prompt string) (string, error) {
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
