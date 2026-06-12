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

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/star-inc/armi/internal/infrastructure/database"
	"github.com/star-inc/armi/internal/infrastructure/jwtauth"
	"github.com/star-inc/armi/internal/infrastructure/storage"
	"github.com/star-inc/armi/internal/infrastructure/vector"
	"github.com/star-inc/armi/internal/usecase"
	"github.com/star-inc/armi/pkgs/contract"
	"github.com/spf13/viper"
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

func (m *MockPublisher) PublishEvent(ctx context.Context, eventType string, userID string, payload map[string]interface{}) error {
	// Do nothing in tests
	return nil
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

	return NewServer(userUsecase, fileUsecase, publisher, jwtauth.AuthSchemeBasic, nil, NewEventsHub())
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
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/users/me", bytes.NewReader(regBytes))
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
	_ = userID

	// 2. Upload File (Requires BasicAuth)
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "document.txt")
	_, _ = part.Write([]byte("This is a simple text document content talking about Go programming and vectors."))
	_ = writer.WriteField("tags", "golang,test")
	_ = writer.WriteField("description", "initial description")
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
	if fileRecord.Description != "initial description" {
		t.Fatalf("Expected description 'initial description', got '%s'", fileRecord.Description)
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

	var fileList contract.FileListResponse
	_ = json.Unmarshal(w.Body.Bytes(), &fileList)
	if len(fileList.Items) != 2 {
		t.Fatalf("Expected file list size 2, got %d", len(fileList.Items))
	}
	if fileList.Total != 2 || fileList.Page != 1 || fileList.PageSize != 20 || fileList.TotalPages != 1 {
		t.Fatalf("Unexpected pagination metadata: %+v", fileList)
	}

	// 3.1 List Files - Filter by tag 'golang'
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/files?tag=golang", nil)
	req.SetBasicAuth("testuser", "securepassword")
	server.Engine.ServeHTTP(w, req)

	_ = json.Unmarshal(w.Body.Bytes(), &fileList)
	if len(fileList.Items) != 1 {
		t.Fatalf("Expected 1 file with tag 'golang', got %d", len(fileList.Items))
	}
	if fileList.Items[0].ID != fileRecord.ID {
		t.Fatalf("Expected list item ID '%s', got '%s'", fileRecord.ID, fileList.Items[0].ID)
	}

	// 3.2 List Files - Filter by tag 'python'
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/files?tag=python", nil)
	req.SetBasicAuth("testuser", "securepassword")
	server.Engine.ServeHTTP(w, req)

	_ = json.Unmarshal(w.Body.Bytes(), &fileList)
	if len(fileList.Items) != 1 {
		t.Fatalf("Expected 1 file with tag 'python', got %d", len(fileList.Items))
	}
	if fileList.Items[0].ID != fileRecord2.ID {
		t.Fatalf("Expected list item ID '%s', got '%s'", fileRecord2.ID, fileList.Items[0].ID)
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

	// 5.1 Patch Metadata File
	patchPayload := map[string]interface{}{
		"filename":    "document-renamed.txt",
		"description": "renamed description",
		"tags":        []string{"updated", "golang"},
	}
	patchBytes, _ := json.Marshal(patchPayload)

	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodPatch, "/api/v1/files/"+fileRecord.ID+"/metadata", bytes.NewReader(patchBytes))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("testuser", "securepassword")
	server.Engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Metadata patch failed: status=%d response=%s", w.Code, w.Body.String())
	}

	var patchedRecord contract.FileResponse
	_ = json.Unmarshal(w.Body.Bytes(), &patchedRecord)
	if patchedRecord.Filename != "document-renamed.txt" {
		t.Fatalf("Expected patched filename 'document-renamed.txt', got '%s'", patchedRecord.Filename)
	}
	if patchedRecord.Description != "renamed description" {
		t.Fatalf("Expected patched description 'renamed description', got '%s'", patchedRecord.Description)
	}
	if len(patchedRecord.Tags) != 2 {
		t.Fatalf("Expected patched tags size 2, got %v", patchedRecord.Tags)
	}
	tagSet := map[string]bool{}
	for _, tg := range patchedRecord.Tags {
		tagSet[tg] = true
	}
	if !tagSet["updated"] || !tagSet["golang"] {
		t.Fatalf("Expected patched tags to contain 'updated' and 'golang', got %v", patchedRecord.Tags)
	}

	// 6. Vector Search
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/files/search?q=programming&nlp_expansion=true&expansion_num=2", nil)
	req.SetBasicAuth("testuser", "securepassword")
	server.Engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Vector search failed: status=%d response=%s", w.Code, w.Body.String())
	}

	var searchResponse contract.SearchListResponse
	_ = json.Unmarshal(w.Body.Bytes(), &searchResponse)
	searchResults := searchResponse.Items
	if len(searchResults) != 1 {
		t.Fatalf("Expected search result size 1, got %d", len(searchResults))
	}
	if searchResponse.NLPExpansion {
		t.Fatalf("Expected NLP expansion to be false since global config is disabled")
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
	if len(fileList.Items) != 0 {
		t.Fatalf("Expected file list size 0 after deletion, got %d", len(fileList.Items))
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
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/users/me", bytes.NewReader(regBytes))
	req.Header.Set("Content-Type", "application/json")
	server.Engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Registration failed for mcpuser: %s", w.Body.String())
	}

	// 2. Initialize Streamable HTTP session
	initReqBody := mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      mcp.NewRequestId(1),
		Request: mcp.Request{
			Method: string(mcp.MethodInitialize),
		},
		Params: mcp.InitializeParams{
			ProtocolVersion: "2025-03-26",
			Capabilities:    mcp.ClientCapabilities{},
			ClientInfo: mcp.Implementation{
				Name:    "armi-test-client",
				Version: "1.0.0",
			},
		},
	}
	initBytes, _ := json.Marshal(initReqBody)

	wInit := httptest.NewRecorder()
	initReq, _ := http.NewRequest(http.MethodPost, "/api/v1/mcp", bytes.NewReader(initBytes))
	initReq.Header.Set("Content-Type", "application/json")
	initReq.SetBasicAuth("mcpuser", "securepassword")
	server.Engine.ServeHTTP(wInit, initReq)

	if wInit.Code != http.StatusOK {
		t.Fatalf("Initialize failed: status=%d response=%s", wInit.Code, wInit.Body.String())
	}

	sessionID := wInit.Header().Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatalf("Expected Mcp-Session-Id header in initialize response, got headers=%v", wInit.Header())
	}
	if !strings.Contains(wInit.Body.String(), "\"protocolVersion\"") {
		t.Fatalf("Expected initialize response payload, got: %s", wInit.Body.String())
	}

	// 3. Post tools/list request over the same /mcp endpoint
	listReqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
	}
	listBytes, _ := json.Marshal(listReqBody)

	wMsg := httptest.NewRecorder()
	msgReq, _ := http.NewRequest(http.MethodPost, "/api/v1/mcp", bytes.NewReader(listBytes))
	msgReq.Header.Set("Content-Type", "application/json")
	msgReq.Header.Set("Mcp-Session-Id", sessionID)
	msgReq.SetBasicAuth("mcpuser", "securepassword")
	server.Engine.ServeHTTP(wMsg, msgReq)

	if wMsg.Code != http.StatusOK {
		t.Fatalf("Expected tools/list to succeed, got status %d: %s", wMsg.Code, wMsg.Body.String())
	}
	if !strings.Contains(wMsg.Body.String(), "list_files") || !strings.Contains(wMsg.Body.String(), "search_files") || !strings.Contains(wMsg.Body.String(), "read_file") {
		t.Fatalf("Expected tools in Streamable HTTP response, got: %s", wMsg.Body.String())
	}

	// 4. Terminate the session using DELETE /mcp
	wDel := httptest.NewRecorder()
	delReq, _ := http.NewRequest(http.MethodDelete, "/api/v1/mcp", nil)
	delReq.Header.Set("Mcp-Session-Id", sessionID)
	delReq.SetBasicAuth("mcpuser", "securepassword")
	server.Engine.ServeHTTP(wDel, delReq)

	if wDel.Code != http.StatusOK {
		t.Fatalf("Expected session termination to succeed, got status %d: %s", wDel.Code, wDel.Body.String())
	}
}

func TestRegisterDisabledWhenBearerOnly(t *testing.T) {
	t.Setenv("GIN_MODE", "test")

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
	server := NewServer(userUsecase, fileUsecase, publisher, jwtauth.AuthSchemeBearer, nil, NewEventsHub())

	regPayload := map[string]string{
		"username": "blocked-user",
		"password": "securepassword",
	}
	regBytes, _ := json.Marshal(regPayload)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/users/me", bytes.NewReader(regBytes))
	req.Header.Set("Content-Type", "application/json")
	server.Engine.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("Expected register endpoint disabled in bearer-only mode, got status=%d response=%s", w.Code, w.Body.String())
	}
}

func TestEventsSSEStreamsMatchingUserEvents(t *testing.T) {
	server := setupTestEnv(t)

	regPayload := map[string]string{
		"username": "stream-user",
		"password": "securepassword",
	}
	regBytes, _ := json.Marshal(regPayload)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/users/me", bytes.NewReader(regBytes))
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sseReq, _ := http.NewRequest(http.MethodGet, "/events", nil)
	sseReq = sseReq.WithContext(ctx)
	sseReq.SetBasicAuth("stream-user", "securepassword")

	sseRec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.Engine.ServeHTTP(sseRec, sseReq)
		close(done)
	}()

	for i := 0; i < 20; i++ {
		if strings.Contains(sseRec.Body.String(), ": connected") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	server.EventHub.Broadcast(contract.SystemEvent{
		EventID:   "evt-1",
		EventType: "embedding.started",
		UserID:    userID,
		Timestamp: time.Now().Format(time.RFC3339),
		Payload: map[string]interface{}{
			"file_id": "file-1",
		},
	})
	server.EventHub.Broadcast(contract.SystemEvent{
		EventID:   "evt-2",
		EventType: "embedding.completed",
		UserID:    "other-user",
		Timestamp: time.Now().Format(time.RFC3339),
		Payload: map[string]interface{}{
			"file_id": "file-2",
		},
	})

	for i := 0; i < 20; i++ {
		body := sseRec.Body.String()
		if strings.Contains(body, "event: embedding.started") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not exit after cancellation")
	}

	body := sseRec.Body.String()
	if !strings.Contains(body, "event: embedding.started") {
		t.Fatalf("Expected matching event in SSE body, got: %s", body)
	}
	if !strings.Contains(body, "\"file_id\":\"file-1\"") {
		t.Fatalf("Expected event payload in SSE body, got: %s", body)
	}
	if strings.Contains(body, "other-user") || strings.Contains(body, "file-2") {
		t.Fatalf("Expected SSE stream to filter other users, got: %s", body)
	}
}
