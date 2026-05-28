package vector

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/spf13/viper"
	"github.com/supersonictw/armi/internal/infrastructure/database"
	"github.com/supersonictw/armi/pkgs/file"
)

// SQLiteVectorDB implements file.VectorDB using sqlite-vec virtual table.
type SQLiteVectorDB struct{}

// QdrantVectorDB implements file.VectorDB using Qdrant REST API.
type QdrantVectorDB struct {
	URL        string
	Collection string
	Client     *http.Client
}

// NewVectorDB constructs a new file.VectorDB instance based on Viper config.
func NewVectorDB() (file.VectorDB, error) {
	provider := viper.GetString("vector.provider")

	switch provider {
	case "sqlite-vec":
		slog.Info("Initializing sqlite-vec VectorDB")
		return &SQLiteVectorDB{}, nil
	case "qdrant":
		qdrantURL := viper.GetString("vector.qdrant.url")
		collection := viper.GetString("vector.qdrant.collection")
		if qdrantURL == "" {
			qdrantURL = "http://localhost:6333"
		}
		if collection == "" {
			collection = "armi_files"
		}
		slog.Info("Initializing Qdrant VectorDB", "url", qdrantURL, "collection", collection)

		db := &QdrantVectorDB{
			URL:        qdrantURL,
			Collection: collection,
			Client:     &http.Client{Timeout: 10 * time.Second},
		}

		if err := db.ensureCollection(); err != nil {
			return nil, fmt.Errorf("qdrant collection initialization failed: %w", err)
		}
		return db, nil
	default:
		return nil, fmt.Errorf("unsupported vector provider: %s", provider)
	}
}

// toUUID converts a fileID (xid) into a deterministic UUID string for Qdrant points.
func toUUID(fileID string) string {
	hasher := md5.New()
	hasher.Write([]byte(fileID))
	hash := hex.EncodeToString(hasher.Sum(nil))
	return fmt.Sprintf("%s-%s-%s-%s-%s", hash[0:8], hash[8:12], hash[12:16], hash[16:20], hash[20:32])
}

// === SQLiteVectorDB ===

// Insert saves embedding vector into sqlite-vec.
func (s *SQLiteVectorDB) Insert(ctx context.Context, fileID string, embedding []float32) error {
	if database.DB == nil {
		return fmt.Errorf("rdbms database not initialized")
	}

	serialized, serializeErr := sqlite_vec.SerializeFloat32(embedding)
	if serializeErr != nil {
		slog.Error("sqlite-vec serialization failed", "error", serializeErr)
		return fmt.Errorf("failed to serialize vector: %w", serializeErr)
	}

	err := database.DB.WithContext(ctx).Exec(
		"INSERT OR REPLACE INTO file_embeddings(file_id, embedding) VALUES (?, ?)",
		fileID, serialized,
	).Error

	if err != nil {
		slog.Error("sqlite-vec insert failed", "file_id", fileID, "error", err)
		return fmt.Errorf("failed to insert vector into sqlite-vec: %w", err)
	}

	return nil
}

// Copy duplicates a vector from one file record to another.
func (s *SQLiteVectorDB) Copy(ctx context.Context, srcFileID string, destFileID string) error {
	if database.DB == nil {
		return fmt.Errorf("rdbms database not initialized")
	}

	err := database.DB.WithContext(ctx).Exec(
		"INSERT INTO file_embeddings(file_id, embedding) SELECT ?, embedding FROM file_embeddings WHERE file_id = ?",
		destFileID, srcFileID,
	).Error

	if err != nil {
		slog.Error("sqlite-vec copy failed", "src_id", srcFileID, "dest_id", destFileID, "error", err)
		return fmt.Errorf("failed to copy vector in sqlite-vec: %w", err)
	}

	return nil
}

// Search queries sqlite-vec for similar vectors.
func (s *SQLiteVectorDB) Search(ctx context.Context, embedding []float32, limit int) ([]file.SearchResult, error) {
	if database.DB == nil {
		return nil, fmt.Errorf("rdbms database not initialized")
	}

	serialized, serializeErr := sqlite_vec.SerializeFloat32(embedding)
	if serializeErr != nil {
		slog.Error("sqlite-vec serialization failed", "error", serializeErr)
		return nil, fmt.Errorf("failed to serialize vector: %w", serializeErr)
	}

	rows, err := database.DB.WithContext(ctx).Raw(
		"SELECT file_id, distance FROM file_embeddings WHERE embedding MATCH ? AND k = ? ORDER BY distance ASC",
		serialized, limit,
	).Rows()

	if err != nil {
		slog.Error("sqlite-vec search failed", "error", err)
		return nil, fmt.Errorf("failed to query sqlite-vec: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	var results []file.SearchResult
	for rows.Next() {
		var res file.SearchResult
		if err := rows.Scan(&res.FileID, &res.Distance); err != nil {
			slog.Error("sqlite-vec scan row failed", "error", err)
			return nil, fmt.Errorf("failed to scan search result row: %w", err)
		}
		results = append(results, res)
	}

	return results, nil
}

// Delete removes vector data.
func (s *SQLiteVectorDB) Delete(ctx context.Context, fileID string) error {
	if database.DB == nil {
		return fmt.Errorf("rdbms database not initialized")
	}

	err := database.DB.WithContext(ctx).Exec(
		"DELETE FROM file_embeddings WHERE file_id = ?",
		fileID,
	).Error

	if err != nil {
		slog.Error("sqlite-vec delete failed", "file_id", fileID, "error", err)
		return fmt.Errorf("failed to delete vector from sqlite-vec: %w", err)
	}

	return nil
}

// Close releases VectorDB resources.
func (s *SQLiteVectorDB) Close() error {
	return nil
}

// === QdrantVectorDB ===

func (q *QdrantVectorDB) ensureCollection() error {
	base, err := url.Parse(q.URL)
	if err != nil {
		return fmt.Errorf("invalid qdrant url: %w", err)
	}

	checkURL := base.JoinPath("collections", q.Collection).String()
	resp, err := q.Client.Get(checkURL)
	if err == nil {
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == http.StatusOK {
			slog.Info("Qdrant collection already exists", "collection", q.Collection)
			return nil
		}
	}

	createURL := base.JoinPath("collections", q.Collection).String()
	reqBody := map[string]interface{}{
		"vectors": map[string]interface{}{
			"size":     768,
			"distance": "Cosine",
		},
	}
	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPut, createURL, bytes.NewReader(jsonBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	cResp, err := q.Client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to create qdrant collection: %w", err)
	}
	defer func() { _ = cResp.Body.Close() }()

	if cResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(cResp.Body)
		return fmt.Errorf("failed to create collection: status=%s response=%s", cResp.Status, string(body))
	}

	slog.Info("Created Qdrant collection", "collection", q.Collection)
	return nil
}

// Insert saves embedding vector into Qdrant.
func (q *QdrantVectorDB) Insert(ctx context.Context, fileID string, embedding []float32) error {
	base, err := url.Parse(q.URL)
	if err != nil {
		return fmt.Errorf("invalid qdrant url: %w", err)
	}

	targetURL := base.JoinPath("collections", q.Collection, "points").String()
	targetURL = targetURL + "?wait=true"

	pointUUID := toUUID(fileID)
	reqBody := map[string]interface{}{
		"points": []map[string]interface{}{
			{
				"id":     pointUUID,
				"vector": embedding,
				"payload": map[string]interface{}{
					"file_id": fileID,
				},
			},
		},
	}
	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		slog.Error("failed to marshal qdrant request", "error", err)
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, targetURL, bytes.NewReader(jsonBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.Client.Do(req)
	if err != nil {
		slog.Error("qdrant insert failed", "file_id", fileID, "error", err)
		return fmt.Errorf("qdrant request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		slog.Error("qdrant insert returned non-OK status", "status", resp.Status, "response", string(body))
		return fmt.Errorf("qdrant api status: %s", resp.Status)
	}

	return nil
}

// Copy duplicates a vector in Qdrant.
func (q *QdrantVectorDB) Copy(ctx context.Context, srcFileID string, destFileID string) error {
	base, err := url.Parse(q.URL)
	if err != nil {
		return fmt.Errorf("invalid qdrant url: %w", err)
	}

	srcUUID := toUUID(srcFileID)
	targetURL := base.JoinPath("collections", q.Collection, "points", srcUUID).String()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return err
	}

	resp, err := q.Client.Do(req)
	if err != nil {
		slog.Error("qdrant fetch point failed in Copy", "src_id", srcFileID, "error", err)
		return fmt.Errorf("failed to fetch source vector: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		slog.Error("qdrant fetch point returned non-OK status", "status", resp.Status, "response", string(body))
		return fmt.Errorf("qdrant fetch point status: %s", resp.Status)
	}

	var respJSON struct {
		Result struct {
			Vector []float32 `json:"vector"`
		} `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&respJSON); err != nil {
		slog.Error("failed to decode qdrant point fetch response", "error", err)
		return fmt.Errorf("failed to decode response: %w", err)
	}

	err = q.Insert(ctx, destFileID, respJSON.Result.Vector)
	if err != nil {
		slog.Error("qdrant insert copied vector failed", "dest_id", destFileID, "error", err)
		return fmt.Errorf("failed to insert copied vector: %w", err)
	}

	return nil
}

// Search finds similar vectors in Qdrant.
func (q *QdrantVectorDB) Search(ctx context.Context, embedding []float32, limit int) ([]file.SearchResult, error) {
	base, err := url.Parse(q.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid qdrant url: %w", err)
	}

	targetURL := base.JoinPath("collections", q.Collection, "points", "search").String()

	reqBody := map[string]interface{}{
		"vector":       embedding,
		"limit":        limit,
		"with_payload": true,
	}
	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(jsonBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.Client.Do(req)
	if err != nil {
		slog.Error("qdrant search failed", "error", err)
		return nil, fmt.Errorf("qdrant search request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		slog.Error("qdrant search returned non-OK status", "status", resp.Status, "response", string(body))
		return nil, fmt.Errorf("qdrant api status: %s", resp.Status)
	}

	var respJSON struct {
		Result []struct {
			Score   float32 `json:"score"`
			Payload struct {
				FileID string `json:"file_id"`
			} `json:"payload"`
		} `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&respJSON); err != nil {
		return nil, err
	}

	var results []file.SearchResult
	for _, item := range respJSON.Result {
		results = append(results, file.SearchResult{
			FileID:   item.Payload.FileID,
			Distance: 1.0 - item.Score,
		})
	}

	return results, nil
}

// Delete removes vector data from Qdrant.
func (q *QdrantVectorDB) Delete(ctx context.Context, fileID string) error {
	base, err := url.Parse(q.URL)
	if err != nil {
		return fmt.Errorf("invalid qdrant url: %w", err)
	}

	targetURL := base.JoinPath("collections", q.Collection, "points", "delete").String()

	pointUUID := toUUID(fileID)
	reqBody := map[string]interface{}{
		"points": []string{pointUUID},
	}
	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(jsonBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.Client.Do(req)
	if err != nil {
		slog.Error("qdrant delete failed", "file_id", fileID, "error", err)
		return fmt.Errorf("qdrant request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		slog.Error("qdrant delete returned non-OK status", "status", resp.Status, "response", string(body))
		return fmt.Errorf("qdrant api status: %s", resp.Status)
	}

	return nil
}

// Close releases QdrantVectorDB resources.
func (q *QdrantVectorDB) Close() error {
	return nil
}
