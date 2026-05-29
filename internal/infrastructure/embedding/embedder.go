package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/viper"
	"github.com/supersonictw/armi/pkgs/file"
)

// OllamaEmbedder implements file.Embedder interface for Ollama service.
type OllamaEmbedder struct {
	BaseURL string
	Model   string
	Client  *http.Client
}

// OpenAIEmbedder implements file.Embedder interface for OpenAI service.
type OpenAIEmbedder struct {
	BaseURL string
	APIKey  string
	Model   string
	Client  *http.Client
}

// NewEmbedder constructs a new file.Embedder based on Viper configuration.
func NewEmbedder() (file.Embedder, error) {
	provider := viper.GetString("embedding.provider")
	model := viper.GetString("embedding.model")

	switch provider {
	case "ollama":
		baseURL := viper.GetString("embedding.ollama.base_url")
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		slog.Info("Initializing Ollama Embedder", "model", model, "base_url", baseURL)
		return &OllamaEmbedder{
			BaseURL: baseURL,
			Model:   model,
			Client:  &http.Client{Timeout: 30 * time.Second},
		}, nil
	case "openai":
		apiKey := viper.GetString("embedding.openai.api_key")
		baseURL := viper.GetString("embedding.openai.base_url")
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		slog.Info("Initializing OpenAI Embedder", "model", model, "base_url", baseURL)
		return &OpenAIEmbedder{
			BaseURL: baseURL,
			APIKey:  apiKey,
			Model:   model,
			Client:  &http.Client{Timeout: 30 * time.Second},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported embedding provider: %s", provider)
	}
}

// Embed generates a vector embedding for text using Ollama /api/embed API.
func (o *OllamaEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if text == "" {
		return make([]float32, 768), nil
	}

	base, err := url.Parse(o.BaseURL)
	if err != nil {
		slog.Error("failed to parse Ollama base URL", "url", o.BaseURL, "error", err)
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}
	targetURL := base.JoinPath("api", "embed").String()

	reqBody := map[string]interface{}{
		"model": o.Model,
		"input": text,
	}
	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		slog.Error("failed to marshal Ollama request body", "error", err)
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(jsonBytes))
	if err != nil {
		slog.Error("failed to create Ollama request", "error", err)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.Client.Do(req)
	if err != nil {
		slog.Error("failed to call Ollama embed API", "error", err)
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		slog.Error("Ollama embed API returned non-OK status", "status", resp.Status, "response", string(respBytes))
		return nil, fmt.Errorf("api error status: %s", resp.Status)
	}

	var respJSON struct {
		Model      string      `json:"model"`
		Embeddings [][]float32 `json:"embeddings"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&respJSON); err != nil {
		slog.Error("failed to decode Ollama response", "error", err)
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(respJSON.Embeddings) == 0 {
		slog.Error("Ollama response has empty embeddings list")
		return nil, fmt.Errorf("empty embeddings returned")
	}

	return respJSON.Embeddings[0], nil
}

// Embed generates a vector embedding for text using OpenAI /embeddings API.
func (o *OpenAIEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if text == "" {
		return make([]float32, 768), nil
	}

	base, err := url.Parse(o.BaseURL)
	if err != nil {
		slog.Error("failed to parse OpenAI base URL", "url", o.BaseURL, "error", err)
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}
	targetURL := base.JoinPath("embeddings").String()

	reqBody := map[string]interface{}{
		"model": o.Model,
		"input": text,
	}
	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		slog.Error("failed to marshal OpenAI request body", "error", err)
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(jsonBytes))
	if err != nil {
		slog.Error("failed to create OpenAI request", "error", err)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.APIKey)
	}

	resp, err := o.Client.Do(req)
	if err != nil {
		slog.Error("failed to call OpenAI embed API", "error", err)
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		maskedBody := string(respBytes)
		if o.APIKey != "" {
			maskedBody = strings.ReplaceAll(maskedBody, o.APIKey, "***")
		}
		slog.Error("OpenAI embed API returned non-OK status", "status", resp.Status, "response", maskedBody)
		return nil, fmt.Errorf("api error status: %s", resp.Status)
	}

	var respJSON struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&respJSON); err != nil {
		slog.Error("failed to decode OpenAI response", "error", err)
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(respJSON.Data) == 0 {
		slog.Error("OpenAI response has empty data list")
		return nil, fmt.Errorf("empty embeddings returned")
	}

	return respJSON.Data[0].Embedding, nil
}
