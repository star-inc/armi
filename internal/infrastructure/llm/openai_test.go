package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/viper"
)

func TestNewOpenAILLM_Defaults(t *testing.T) {
	viper.Set("llm.openai.api_key", "llm-key")
	viper.Set("llm.openai.base_url", "http://llm-url")
	viper.Set("llm.model", "some-model")

	llm, err := NewOpenAILLM()
	if err != nil {
		t.Fatalf("failed to create OpenAILLM: %v", err)
	}

	o, ok := llm.(*OpenAILLM)
	if !ok {
		t.Fatalf("expected *OpenAILLM, got %T", llm)
	}
	if o.ModelName != "some-model" {
		t.Errorf("expected model some-model, got %s", o.ModelName)
	}
}

func TestNewOpenAILLM_Fallbacks(t *testing.T) {
	viper.Set("llm.openai.api_key", "")
	viper.Set("llm.openai.base_url", "")
	viper.Set("llm.model", "")
	viper.Set("embedding.openai.api_key", "embed-key")
	viper.Set("embedding.openai.base_url", "http://embed-url")

	llm, err := NewOpenAILLM()
	if err != nil {
		t.Fatalf("failed to create OpenAILLM: %v", err)
	}

	o, ok := llm.(*OpenAILLM)
	if !ok {
		t.Fatalf("expected *OpenAILLM, got %T", llm)
	}
	if o.ModelName != "@default/anthropic/claude-haiku-4-5" {
		t.Errorf("expected default model, got %s", o.ModelName)
	}
}

func TestOpenAILLM_GenerateQueries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("expected path /chat/completions, got %s", r.URL.Path)
		}

		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatal(err)
		}
		if reqBody["model"] != "test-model" {
			t.Errorf("expected test-model, got %v", reqBody["model"])
		}

		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "query number one\n- query number two\n* query number three\n  \nquery number four",
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	viper.Set("llm.openai.api_key", "key")
	viper.Set("llm.openai.base_url", server.URL)
	viper.Set("llm.model", "test-model")

	llm, err := NewOpenAILLM()
	if err != nil {
		t.Fatal(err)
	}

	// 1. Success path
	queries, err := llm.GenerateQueries(context.Background(), "original search query", 3)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Should be limited to 3 queries (the num parameter)
	if len(queries) != 3 {
		t.Fatalf("expected 3 queries, got %d: %v", len(queries), queries)
	}
	if queries[0] != "query number one" || queries[1] != "query number two" || queries[2] != "query number three" {
		t.Errorf("unexpected queries returned: %v", queries)
	}

	// 2. Empty query returns empty slice
	emptyQueries, err := llm.GenerateQueries(context.Background(), "", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(emptyQueries) != 0 {
		t.Errorf("expected empty queries, got %v", emptyQueries)
	}
}

func TestOpenAILLM_PerformOCR(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatal(err)
		}

		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "transcribed text from image",
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	viper.Set("llm.openai.api_key", "key")
	viper.Set("llm.openai.base_url", server.URL)
	viper.Set("llm.model", "test-model")
	viper.Set("llm.ocr.enabled", true)

	llm, err := NewOpenAILLM()
	if err != nil {
		t.Fatal(err)
	}

	// 1. Empty image returns empty string
	emptyOCR, err := llm.PerformOCR(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if emptyOCR != "" {
		t.Errorf("expected empty string for empty OCR, got %s", emptyOCR)
	}

	// 2. OCR disabled returns empty string
	viper.Set("llm.ocr.enabled", false)
	disabledOCR, err := llm.PerformOCR(context.Background(), "some-base64-data")
	if err != nil {
		t.Fatal(err)
	}
	if disabledOCR != "" {
		t.Errorf("expected empty string when OCR is disabled, got %s", disabledOCR)
	}
	viper.Set("llm.ocr.enabled", true) // restore

	// 3. Success path
	res, err := llm.PerformOCR(context.Background(), "some-base64-data")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if res != "transcribed text from image" {
		t.Errorf("expected transcribed text from image, got %s", res)
	}
}
