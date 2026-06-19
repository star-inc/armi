package rerank

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/viper"
)

func TestHTTPRerankerCohere(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST request, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer mock-key" {
			t.Errorf("expected Bearer authorization header, got %s", r.Header.Get("Authorization"))
		}

		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatal(err)
		}
		if reqBody["model"] != "rerank-model" {
			t.Errorf("expected model rerank-model, got %v", reqBody["model"])
		}

		resp := map[string]interface{}{
			"results": []map[string]interface{}{
				{"index": 0, "relevance_score": 0.95},
				{"index": 1, "relevance_score": 0.85},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	viper.Set("rerank.provider", "cohere")
	viper.Set("rerank.model", "rerank-model")
	viper.Set("rerank.api_key", "mock-key")
	viper.Set("rerank.base_url", server.URL)

	client, err := NewReranker()
	if err != nil {
		t.Fatal(err)
	}

	results, err := client.Rerank(context.Background(), "query", []string{"doc1", "doc2"})
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Index != 0 || results[0].RelevanceScore != 0.95 {
		t.Errorf("unexpected first result: %+v", results[0])
	}
}

func TestHTTPRerankerLlamaCPP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rerank" {
			t.Errorf("expected path /v1/rerank, got %s", r.URL.Path)
		}

		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatal(err)
		}
		if reqBody["model"] != "llama-model" {
			t.Errorf("expected model llama-model, got %v", reqBody["model"])
		}

		resp := map[string]interface{}{
			"results": []map[string]interface{}{
				{"index": 0, "relevance_score": 0.99},
				{"index": 1, "relevance_score": 0.01},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	viper.Set("rerank.provider", "llamacpp")
	viper.Set("rerank.model", "llama-model")
	viper.Set("rerank.api_key", "")
	viper.Set("rerank.base_url", server.URL+"/v1/rerank")

	client, err := NewReranker()
	if err != nil {
		t.Fatal(err)
	}

	results, err := client.Rerank(context.Background(), "query", []string{"doc1", "doc2"})
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Index != 0 || results[0].RelevanceScore != 0.99 {
		t.Errorf("unexpected first result: %+v", results[0])
	}
}
