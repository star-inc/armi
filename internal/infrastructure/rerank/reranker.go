package rerank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/spf13/viper"
	"github.com/star-inc/armi/pkgs/file"
)

type HTTPReranker struct {
	Provider string
	BaseURL  string
	APIKey   string
	Model    string
	Client   *http.Client
}

func NewReranker() (file.Reranker, error) {
	provider := viper.GetString("rerank.provider")
	if provider == "" {
		provider = "llamacpp"
	}
	model := viper.GetString("rerank.model")
	apiKey := viper.GetString("rerank.api_key")
	baseURL := viper.GetString("rerank.base_url")

	if baseURL == "" {
		switch provider {
		case "llamacpp":
			baseURL = "http://localhost:8080/v1/rerank"
		case "cohere":
			baseURL = "https://api.cohere.com/v1/rerank"
		case "jina":
			baseURL = "https://api.jina.ai/v1/rerank"
		default:
			return nil, fmt.Errorf("unsupported rerank provider: %s", provider)
		}
	}

	slog.Info("Initializing Reranker", "provider", provider, "model", model, "base_url", baseURL)
	return &HTTPReranker{
		Provider: provider,
		BaseURL:  baseURL,
		APIKey:   apiKey,
		Model:    model,
		Client:   &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (r *HTTPReranker) Rerank(ctx context.Context, query string, documents []string) ([]file.RerankResult, error) {
	if len(documents) == 0 {
		return []file.RerankResult{}, nil
	}

	reqBody := map[string]interface{}{
		"model":     r.Model,
		"query":     query,
		"documents": documents,
	}
	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		slog.Error("failed to marshal rerank request body", "error", err)
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.BaseURL, bytes.NewReader(jsonBytes))
	if err != nil {
		slog.Error("failed to create rerank request", "error", err)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if r.APIKey != "" {
		if r.Provider == "jina" || r.Provider == "cohere" {
			req.Header.Set("Authorization", "Bearer "+r.APIKey)
		}
	}

	resp, err := r.Client.Do(req)
	if err != nil {
		slog.Error("failed to call rerank API", "error", err)
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		slog.Error("rerank API returned non-OK status", "status", resp.Status, "response", string(respBytes))
		return nil, fmt.Errorf("api error status: %s", resp.Status)
	}

	var standardResp struct {
		Results []struct {
			Index          int     `json:"index"`
			RelevanceScore float32 `json:"relevance_score"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&standardResp); err != nil {
		slog.Error("failed to decode rerank response", "error", err)
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	results := make([]file.RerankResult, 0, len(standardResp.Results))
	for _, sr := range standardResp.Results {
		results = append(results, file.RerankResult{
			Index:          sr.Index,
			RelevanceScore: sr.RelevanceScore,
		})
	}

	return results, nil
}
