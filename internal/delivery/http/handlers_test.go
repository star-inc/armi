package http

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/viper"
	"github.com/supersonictw/armi/internal/infrastructure/database"
	"github.com/supersonictw/armi/internal/infrastructure/storage"
	"github.com/supersonictw/armi/internal/infrastructure/vector"
	"github.com/supersonictw/armi/internal/usecase"
	"github.com/supersonictw/armi/pkgs/contract"
)

// MockEmbedder implements file.Embedder for testing.
type MockEmbedder struct{}

func (m *MockEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vec := make([]float32, 768)
	vec[0] = 0.99
	return vec, nil
}

// MockPublisher implements file.EventPublisher for testing.
type MockPublisher struct{}

func (m *MockPublisher) PublishEvent(ctx context.Context, eventType string, userID string, payload map[string]interface{}) {
	// Do nothing in tests
}

func (m *MockPublisher) Close() error {
	return nil
}

func setupTestEnv(t *testing.T) *Server {
	t.Helper()

	viper.Set("db.driver", "sqlite")
	viper.Set("db.sqlite.path", ":memory:")
	viper.Set("storage.scheme", "memory")
	viper.Set("vector.provider", "sqlite-vec")
	viper.Set("rabbitmq.enabled", false)

	db, err := database.InitDB()
	if err != nil {
		t.Fatalf("failed to init database: %v", err)
	}

	store, err := storage.NewOpenDALStorage()
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}

	embedder := &MockEmbedder{}
	vectorDB, err := vector.NewVectorDB()
	if err != nil {
		t.Fatalf("failed to init vector db: %v", err)
	}

	publisher := &MockPublisher{}

	userRepo := database.NewGormUserRepository(db)
	fileRepo := database.NewGormFileRepository(db)

	userUsecase := usecase.NewUserUsecase(userRepo, publisher)
	fileUsecase := usecase.NewFileUsecase(fileRepo, store, embedder, vectorDB, publisher)

	return NewServer(userUsecase, fileUsecase, publisher)
}

func TestHandlersFlow(t *testing.T) {
	server := setupTestEnv(t)

	// 1. Register User
	regPayload := map[string]string{
		"username": "testuser",
		"password": "securepassword",
	}
	regBytes, _ := json.Marshal(regPayload)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/users/register", bytes.NewReader(regBytes))
	req.Header.Set("Content-Type", "application/json")
	server.Engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Registration failed: status=%d response=%s", w.Code, w.Body.String())
	}

	var userJSON map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &userJSON)
	userID := userJSON["id"].(string)
	if userID == "" {
		t.Fatalf("Expected non-empty user ID in response")
	}

	// 2. Upload File (Requires BasicAuth)
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "document.txt")
	_, _ = part.Write([]byte("This is a simple text document content talking about Go programming and vectors."))
	_ = writer.Close()

	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodPost, "/api/v1/files", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.SetBasicAuth("testuser", "securepassword")
	server.Engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("File upload failed: status=%d response=%s", w.Code, w.Body.String())
	}

	var fileRecord contract.FileResponse
	_ = json.Unmarshal(w.Body.Bytes(), &fileRecord)
	if fileRecord.ID == "" {
		t.Fatalf("Expected non-empty file record ID in response")
	}
	if fileRecord.Filename != "document.txt" {
		t.Fatalf("Expected filename 'document.txt', got '%s'", fileRecord.Filename)
	}

	// 3. List Files (Requires BasicAuth)
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/files", nil)
	req.SetBasicAuth("testuser", "securepassword")
	server.Engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("File listing failed: status=%d response=%s", w.Code, w.Body.String())
	}

	var fileList []contract.FileResponse
	_ = json.Unmarshal(w.Body.Bytes(), &fileList)
	if len(fileList) != 1 {
		t.Fatalf("Expected file list size 1, got %d", len(fileList))
	}
	if fileList[0].ID != fileRecord.ID {
		t.Fatalf("Expected list item ID '%s', got '%s'", fileRecord.ID, fileList[0].ID)
	}

	// 4. Download File
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/files/"+fileRecord.ID, nil)
	req.SetBasicAuth("testuser", "securepassword")
	server.Engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("File download failed: status=%d response=%s", w.Code, w.Body.String())
	}
	if w.Body.String() != "This is a simple text document content talking about Go programming and vectors." {
		t.Fatalf("Expected file content mismatch: got '%s'", w.Body.String())
	}

	// 5. Metadata File
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/files/"+fileRecord.ID+"/metadata", nil)
	req.SetBasicAuth("testuser", "securepassword")
	server.Engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Metadata fetch failed: status=%d response=%s", w.Code, w.Body.String())
	}

	// 6. Vector Search
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/files/search?q=programming", nil)
	req.SetBasicAuth("testuser", "securepassword")
	server.Engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Vector search failed: status=%d response=%s", w.Code, w.Body.String())
	}

	var searchResults []map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &searchResults)
	if len(searchResults) != 1 {
		t.Fatalf("Expected search result size 1, got %d", len(searchResults))
	}
	if searchResults[0]["id"].(string) != fileRecord.ID {
		t.Fatalf("Expected search match ID '%s', got '%s'", fileRecord.ID, searchResults[0]["id"])
	}

	// 7. Delete File
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodDelete, "/api/v1/files/"+fileRecord.ID, nil)
	req.SetBasicAuth("testuser", "securepassword")
	server.Engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("File deletion failed: status=%d response=%s", w.Code, w.Body.String())
	}

	// Verify it's deleted from list
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/files", nil)
	req.SetBasicAuth("testuser", "securepassword")
	server.Engine.ServeHTTP(w, req)

	_ = json.Unmarshal(w.Body.Bytes(), &fileList)
	if len(fileList) != 0 {
		t.Fatalf("Expected file list size 0 after deletion, got %d", len(fileList))
	}
}
