package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/spf13/viper"
)

func TestNewEmbedder_Ollama(t *testing.T) {
	viper.Set("embedding.provider", "ollama")
	viper.Set("embedding.model", "nomic-embed-text")
	viper.Set("embedding.ollama.base_url", "http://ollama-host:11434")

	embedder, err := NewEmbedder()
	if err != nil {
		t.Fatalf("failed to create Ollama embedder: %v", err)
	}

	oe, ok := embedder.(*OllamaEmbedder)
	if !ok {
		t.Fatalf("expected *OllamaEmbedder, got %T", embedder)
	}
	if oe.Model != "nomic-embed-text" || oe.BaseURL != "http://ollama-host:11434" {
		t.Errorf("incorrect fields: Model=%s, BaseURL=%s", oe.Model, oe.BaseURL)
	}
}

func TestNewEmbedder_OpenAI(t *testing.T) {
	viper.Set("embedding.provider", "openai")
	viper.Set("embedding.model", "text-embedding-3-small")
	viper.Set("embedding.openai.base_url", "https://custom-openai-api")
	viper.Set("embedding.openai.api_key", "secret-key-abc")

	embedder, err := NewEmbedder()
	if err != nil {
		t.Fatalf("failed to create OpenAI embedder: %v", err)
	}

	oae, ok := embedder.(*OpenAIEmbedder)
	if !ok {
		t.Fatalf("expected *OpenAIEmbedder, got %T", embedder)
	}
	if oae.Model != "text-embedding-3-small" || oae.BaseURL != "https://custom-openai-api" || oae.APIKey != "secret-key-abc" {
		t.Errorf("incorrect fields: Model=%s, BaseURL=%s, APIKey=%s", oae.Model, oae.BaseURL, oae.APIKey)
	}
}

func TestNewEmbedder_Invalid(t *testing.T) {
	viper.Set("embedding.provider", "invalid")
	_, err := NewEmbedder()
	if err == nil {
		t.Error("expected error for invalid provider")
	}
}

func TestOllamaEmbedder_Embed(t *testing.T) {
	// Mock Ollama server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST method, got %s", r.Method)
		}
		if r.URL.Path != "/api/embed" {
			t.Errorf("expected path /api/embed, got %s", r.URL.Path)
		}

		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req["model"] != "ollama-model" {
			t.Errorf("expected model ollama-model, got %v", req["model"])
		}
		if req["input"] != "hello world" {
			t.Errorf("expected input 'hello world', got %v", req["input"])
		}

		resp := map[string]interface{}{
			"embeddings": [][]float32{
				{0.1, 0.2, 0.3},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	oe := &OllamaEmbedder{
		BaseURL: server.URL,
		Model:   "ollama-model",
		Client:  &http.Client{Timeout: 2 * time.Second},
	}

	// 1. Valid input
	res, err := oe.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(res) != 3 || res[0] != 0.1 || res[1] != 0.2 || res[2] != 0.3 {
		t.Errorf("unexpected embedding: %v", res)
	}

	// 2. Empty input returns zero vector of configured dimension
	viper.Set("embedding.dimension", 5)
	defer viper.Set("embedding.dimension", 0) // reset
	resEmpty, err := oe.Embed(context.Background(), "")
	if err != nil {
		t.Fatalf("expected no error for empty input, got %v", err)
	}
	if len(resEmpty) != 5 {
		t.Errorf("expected zero vector of dimension 5, got length %d", len(resEmpty))
	}
	for _, val := range resEmpty {
		if val != 0 {
			t.Errorf("expected zero elements in vector, got %f", val)
		}
	}
}

func TestOpenAIEmbedder_Embed(t *testing.T) {
	// Mock OpenAI server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST method, got %s", r.Method)
		}
		if r.URL.Path != "/embeddings" {
			t.Errorf("expected path /embeddings, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("expected Bearer token, got %s", r.Header.Get("Authorization"))
		}

		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req["model"] != "openai-model" {
			t.Errorf("expected model openai-model, got %v", req["model"])
		}
		if req["input"] != "hello world" {
			t.Errorf("expected input 'hello world', got %v", req["input"])
		}

		resp := map[string]interface{}{
			"data": []map[string]interface{}{
				{
					"embedding": []float32{0.4, 0.5, 0.6},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	oae := &OpenAIEmbedder{
		BaseURL: server.URL,
		APIKey:  "test-key",
		Model:   "openai-model",
		Client:  &http.Client{Timeout: 2 * time.Second},
	}

	// 1. Valid input
	res, err := oae.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(res) != 3 || res[0] != 0.4 || res[1] != 0.5 || res[2] != 0.6 {
		t.Errorf("unexpected embedding: %v", res)
	}

	// 2. Empty input
	viper.Set("embedding.dimension", 4)
	defer viper.Set("embedding.dimension", 0)
	resEmpty, err := oae.Embed(context.Background(), "")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(resEmpty) != 4 {
		t.Errorf("expected zero vector of dimension 4, got length %d", len(resEmpty))
	}
}
