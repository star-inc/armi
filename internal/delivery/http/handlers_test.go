package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/supersonictw/armi/internal/infrastructure/database"
	"github.com/supersonictw/armi/internal/infrastructure/jwtauth"
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

// MockLLM implements file.LLM for testing.
type MockLLM struct{}

func (m *MockLLM) GenerateQueries(ctx context.Context, query string, num int) ([]string, error) {
	var result []string
	for i := 1; i <= num; i++ {
		result = append(result, fmt.Sprintf("%s alternative %d", query, i))
	}
	return result, nil
}

func (m *MockLLM) PerformOCR(ctx context.Context, imageBase64 string) (string, error) {
	return "mocked ocr text", nil
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
	llmService := &MockLLM{}

	userRepo := database.NewGormUserRepository(db)
	fileRepo := database.NewGormFileRepository(db)

	userUsecase := usecase.NewUserUsecase(userRepo, publisher)
	fileUsecase := usecase.NewFileUsecase(fileRepo, store, embedder, vectorDB, llmService, publisher, nil)

	return NewServer(userUsecase, fileUsecase, publisher, jwtauth.AuthSchemeBasic, nil)
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
	_ = writer.WriteField("tags", "golang,test")
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
	if len(fileRecord.Tags) != 2 || fileRecord.Tags[0] != "golang" || fileRecord.Tags[1] != "test" {
		t.Fatalf("Expected tags ['golang', 'test'], got %v", fileRecord.Tags)
	}

	// 2.2 Upload Second File (Requires BasicAuth)
	body2 := &bytes.Buffer{}
	writer2 := multipart.NewWriter(body2)
	part2, _ := writer2.CreateFormFile("file", "python_doc.txt")
	_, _ = part2.Write([]byte("Unrelated text about cookies and cakes."))
	_ = writer2.WriteField("tags", "python")
	_ = writer2.Close()

	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodPost, "/api/v1/files", body2)
	req2.Header.Set("Content-Type", writer2.FormDataContentType())
	req2.SetBasicAuth("testuser", "securepassword")
	server.Engine.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("Second file upload failed: %s", w2.Body.String())
	}

	var fileRecord2 contract.FileResponse
	_ = json.Unmarshal(w2.Body.Bytes(), &fileRecord2)

	// 3. List Files - No Filter (Requires BasicAuth)
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/files", nil)
	req.SetBasicAuth("testuser", "securepassword")
	server.Engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("File listing failed: status=%d response=%s", w.Code, w.Body.String())
	}

	var fileList []contract.FileResponse
	_ = json.Unmarshal(w.Body.Bytes(), &fileList)
	if len(fileList) != 2 {
		t.Fatalf("Expected file list size 2, got %d", len(fileList))
	}

	// 3.1 List Files - Filter by tag 'golang'
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/files?tag=golang", nil)
	req.SetBasicAuth("testuser", "securepassword")
	server.Engine.ServeHTTP(w, req)

	_ = json.Unmarshal(w.Body.Bytes(), &fileList)
	if len(fileList) != 1 {
		t.Fatalf("Expected 1 file with tag 'golang', got %d", len(fileList))
	}
	if fileList[0].ID != fileRecord.ID {
		t.Fatalf("Expected list item ID '%s', got '%s'", fileRecord.ID, fileList[0].ID)
	}

	// 3.2 List Files - Filter by tag 'python'
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/files?tag=python", nil)
	req.SetBasicAuth("testuser", "securepassword")
	server.Engine.ServeHTTP(w, req)

	_ = json.Unmarshal(w.Body.Bytes(), &fileList)
	if len(fileList) != 1 {
		t.Fatalf("Expected 1 file with tag 'python', got %d", len(fileList))
	}
	if fileList[0].ID != fileRecord2.ID {
		t.Fatalf("Expected list item ID '%s', got '%s'", fileRecord2.ID, fileList[0].ID)
	}

	// 3.3 List Files - Filter by non-existent tag 'ruby'
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/files?tag=ruby", nil)
	req.SetBasicAuth("testuser", "securepassword")
	server.Engine.ServeHTTP(w, req)

	_ = json.Unmarshal(w.Body.Bytes(), &fileList)
	// 3.4 Delete python file to avoid mock vector search collisions
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodDelete, "/api/v1/files/"+fileRecord2.ID, nil)
	req.SetBasicAuth("testuser", "securepassword")
	server.Engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Failed to delete second test file: %s", w.Body.String())
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
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/files/search?q=programming&nlp_expansion=true&expansion_num=2", nil)
	req.SetBasicAuth("testuser", "securepassword")
	server.Engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Vector search failed: status=%d response=%s", w.Code, w.Body.String())
	}

	var searchResults []contract.SearchResponseItem
	_ = json.Unmarshal(w.Body.Bytes(), &searchResults)
	if len(searchResults) != 1 {
		t.Fatalf("Expected search result size 1, got %d", len(searchResults))
	}
	if searchResults[0].ID != fileRecord.ID {
		t.Fatalf("Expected search match ID '%s', got '%s'", fileRecord.ID, searchResults[0].ID)
	}
	if searchResults[0].SourceQuery == "" {
		t.Fatalf("Expected search item to have a non-empty source_query")
	}
	if searchResults[0].ChunkText == "" {
		t.Fatalf("Expected search item to have a non-empty chunk_text")
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

func TestMCPFlow(t *testing.T) {
	server := setupTestEnv(t)

	// 1. Register User
	regPayload := map[string]string{
		"username": "mcpuser",
		"password": "securepassword",
	}
	regBytes, _ := json.Marshal(regPayload)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/users/register", bytes.NewReader(regBytes))
	req.Header.Set("Content-Type", "application/json")
	server.Engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Registration failed for mcpuser: %s", w.Body.String())
	}

	// 2. Open background SSE Connection
	sseReq, _ := http.NewRequest(http.MethodGet, "/api/v1/mcp", nil)
	sseReq.SetBasicAuth("mcpuser", "securepassword")
	sseRec := httptest.NewRecorder()

	ctx, cancel := context.WithCancel(context.Background())
	sseReq = sseReq.WithContext(ctx)

	go func() {
		server.Engine.ServeHTTP(sseRec, sseReq)
	}()

	// Wait for SSE server to write endpoint event
	var bodyStr string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		bodyStr = sseRec.Body.String()
		if strings.Contains(bodyStr, "event: endpoint") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !strings.Contains(bodyStr, "event: endpoint") {
		cancel()
		t.Fatalf("Expected endpoint event in SSE stream, got: %s", bodyStr)
	}

	// Extract sessionId
	idx := strings.Index(bodyStr, "sessionId=")
	if idx == -1 {
		cancel()
		t.Fatalf("Could not find sessionId in body: %s", bodyStr)
	}
	sessionID := strings.TrimSpace(bodyStr[idx+len("sessionId="):])
	sessionID = strings.Split(sessionID, "\n")[0]
	sessionID = strings.TrimRight(sessionID, "\r")

	// 3. Post tools/list message
	listReqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
	}
	listBytes, _ := json.Marshal(listReqBody)

	wMsg := httptest.NewRecorder()
	msgReq, _ := http.NewRequest(http.MethodPost, "/api/v1/mcp/message?sessionId="+sessionID, bytes.NewReader(listBytes))
	msgReq.Header.Set("Content-Type", "application/json")
	msgReq.SetBasicAuth("mcpuser", "securepassword")
	server.Engine.ServeHTTP(wMsg, msgReq)

	if wMsg.Code != http.StatusAccepted && wMsg.Code != http.StatusOK {
		cancel()
		t.Fatalf("Expected message post accepted, got status %d: %s", wMsg.Code, wMsg.Body.String())
	}

	// Wait for server response via SSE channel
	var finalBody string
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		finalBody = sseRec.Body.String()
		if strings.Contains(finalBody, "list_files") || strings.Contains(finalBody, "search_files") || strings.Contains(finalBody, "read_file") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !strings.Contains(finalBody, "list_files") || !strings.Contains(finalBody, "search_files") || !strings.Contains(finalBody, "read_file") {
		cancel()
		t.Fatalf("Expected tools in SSE body, got: %s", finalBody)
	}

	cancel()
}
