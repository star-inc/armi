package llm

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	openai "github.com/sashabaranov/go-openai"
	"github.com/spf13/viper"
	"github.com/supersonictw/armi/pkgs/file"
)

// OpenAILLM implements file.LLM using github.com/sashabaranov/go-openai.
type OpenAILLM struct {
	Client    *openai.Client
	ModelName string
}

// NewOpenAILLM initializes the OpenAI LLM client based on Viper configuration.
func NewOpenAILLM() (file.LLM, error) {
	apiKey := viper.GetString("llm.openai.api_key")
	if apiKey == "" {
		// Fallback to embedding apiKey if llm one is empty
		apiKey = viper.GetString("embedding.openai.api_key")
	}

	baseURL := viper.GetString("llm.openai.base_url")
	if baseURL == "" {
		baseURL = viper.GetString("embedding.openai.base_url")
	}

	modelName := viper.GetString("llm.model")
	if modelName == "" {
		modelName = "gpt-4o-mini"
	}

	slog.Info("Initializing OpenAI LLM client for NLP search expansion", "model", modelName, "base_url", baseURL)

	config := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		config.BaseURL = baseURL
	}

	client := openai.NewClientWithConfig(config)
	return &OpenAILLM{
		Client:    client,
		ModelName: modelName,
	}, nil
}

// GenerateQueries generates alternative queries for expansion.
func (l *OpenAILLM) GenerateQueries(ctx context.Context, query string, num int) ([]string, error) {
	if query == "" || num <= 0 {
		return []string{}, nil
	}

	prompt := fmt.Sprintf("請針對下述搜尋關鍵字/語句，生成 %d 個不同的同義/搜尋問法（每行返回一個問法，不需編號或任何解釋）。\n\n搜尋關鍵字：%s", num, query)

	resp, err := l.Client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: l.ModelName,
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleUser,
				Content: prompt,
			},
		},
		Temperature: 0.3,
	})
	if err != nil {
		slog.Error("OpenAI Chat Completion request failed in query expansion", "error", err)
		return nil, fmt.Errorf("failed to generate alternative queries: %w", err)
	}

	if len(resp.Choices) == 0 {
		slog.Warn("OpenAI returned no chat completion choices for query expansion")
		return []string{}, nil
	}

	content := resp.Choices[0].Message.Content
	lines := strings.Split(content, "\n")
	var result []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Strip markdown markers or lists if LLM still outputs them
		trimmed = strings.TrimPrefix(trimmed, "- ")
		trimmed = strings.TrimPrefix(trimmed, "* ")
		trimmed = strings.TrimSpace(trimmed)
		// Skip empty lines
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}

	// Limit to requested number of expansions
	if len(result) > num {
		result = result[:num]
	}

	slog.Info("Successfully generated expanded queries", "original", query, "expanded", result)
	return result, nil
}

// PerformOCR performs OCR on a base64 encoded image using OpenAI Vision API.
func (l *OpenAILLM) PerformOCR(ctx context.Context, imageBase64 string) (string, error) {
	if imageBase64 == "" {
		return "", nil
	}

	resp, err := l.Client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: l.ModelName,
		Messages: []openai.ChatCompletionMessage{
			{
				Role: openai.ChatMessageRoleUser,
				MultiContent: []openai.ChatMessagePart{
					{
						Type: openai.ChatMessagePartTypeText,
						Text: "請精確且不做任何解釋地，將這張圖片中的所有文字辨識並重現出來。保持原本的換行與格式。",
					},
					{
						Type: openai.ChatMessagePartTypeImageURL,
						ImageURL: &openai.ChatMessageImageURL{
							URL: "data:image/png;base64," + imageBase64,
						},
					},
				},
			},
		},
		Temperature: 0.1,
	})
	if err != nil {
		slog.Error("OpenAI Chat Completion request failed in PerformOCR", "error", err)
		return "", fmt.Errorf("failed to perform OCR via OpenAI: %w", err)
	}

	if len(resp.Choices) == 0 {
		slog.Warn("OpenAI returned no chat completion choices for PerformOCR")
		return "", nil
	}

	return strings.TrimSpace(resp.Choices[0].Message.Content), nil
}
