package llm

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	openai "github.com/sashabaranov/go-openai"
	"github.com/spf13/viper"
	"github.com/star-inc/armi/pkgs/file"
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
		modelName = "@default/anthropic/claude-haiku-4-5"
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

	prompt := fmt.Sprintf("Generate %d different search queries or synonymous phrasings for the following search query/phrase. Return one phrasing per line, without any numbering or explanation.\n\nSearch query: %s", num, query)

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
	if !viper.GetBool("llm.ocr.enabled") {
		slog.Debug("OCR is disabled by configuration", "config_key", "llm.ocr.enabled")
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
						Text: "Precisely recognize and transcribe all text in this image without any explanation. Maintain the original line breaks and formatting.",
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
