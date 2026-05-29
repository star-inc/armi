package vector

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
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

func hashToRowID(s string) int64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	// Mask the sign bit to guarantee a positive signed 64-bit integer
	return int64(h.Sum64() & 0x7fffffffffffffff)
}

// Insert saves embedding vector into sqlite-vec.
func (s *SQLiteVectorDB) Insert(ctx context.Context, fileID string, chunkIndex int, text string, embedding []float32) error {
	if database.DB == nil {
		return fmt.Errorf("rdbms database not initialized")
	}

	serialized, serializeErr := sqlite_vec.SerializeFloat32(embedding)
	if serializeErr != nil {
		slog.Error("sqlite-vec serialization failed", "error", serializeErr)
		return fmt.Errorf("failed to serialize vector: %w", serializeErr)
	}

	chunkID := fmt.Sprintf("%s_%d", fileID, chunkIndex)
	rowID := hashToRowID(chunkID)
	err := database.DB.WithContext(ctx).Exec(
		"INSERT OR REPLACE INTO file_embeddings(rowid, chunk_id, file_id, text, embedding) VALUES (?, ?, ?, ?, ?)",
		rowID, chunkID, fileID, text, serialized,
	).Error

	if err != nil {
		slog.Error("sqlite-vec insert failed", "chunk_id", chunkID, "file_id", fileID, "rowid", rowID, "error", err)
		return fmt.Errorf("failed to insert vector into sqlite-vec: %w", err)
	}

	return nil
}

// Copy duplicates a vector from one file record to another.
func (s *SQLiteVectorDB) Copy(ctx context.Context, srcFileID string, destFileID string) error {
	if database.DB == nil {
		return fmt.Errorf("rdbms database not initialized")
	}

	// Query all chunks for srcFileID
	rows, err := database.DB.WithContext(ctx).Raw(
		"SELECT chunk_id, text, embedding FROM file_embeddings WHERE file_id = ?",
		srcFileID,
	).Rows()
	if err != nil {
		slog.Error("sqlite-vec copy failed (select)", "src_id", srcFileID, "error", err)
		return fmt.Errorf("failed to query source vectors: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Iterate and insert each duplicated chunk with a deterministic rowid
	for rows.Next() {
		var oldChunkID, text string
		var embedding []byte

		if err := rows.Scan(&oldChunkID, &text, &embedding); err != nil {
			slog.Error("sqlite-vec copy scan failed", "src_id", srcFileID, "error", err)
			return fmt.Errorf("failed to scan source vector row: %w", err)
		}

		newChunkID := strings.Replace(oldChunkID, srcFileID, destFileID, 1)
		rowID := hashToRowID(newChunkID)

		err := database.DB.WithContext(ctx).Exec(
			"INSERT OR REPLACE INTO file_embeddings(rowid, chunk_id, file_id, text, embedding) VALUES (?, ?, ?, ?, ?)",
			rowID, newChunkID, destFileID, text, embedding,
		).Error
		if err != nil {
			slog.Error("sqlite-vec copy insert failed", "dest_id", destFileID, "chunk_id", newChunkID, "error", err)
			return fmt.Errorf("failed to insert copy vector: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlite-vec copy rows iteration error: %w", err)
	}

	return nil
}

// Search queries sqlite-vec for similar vectors and keywords.
func (s *SQLiteVectorDB) Search(ctx context.Context, embedding []float32, keywords []string, limit int) ([]file.SearchResult, error) {
	if database.DB == nil {
		return nil, fmt.Errorf("rdbms database not initialized")
	}

	serialized, serializeErr := sqlite_vec.SerializeFloat32(embedding)
	if serializeErr != nil {
		slog.Error("sqlite-vec serialization failed", "error", serializeErr)
		return nil, fmt.Errorf("failed to serialize vector: %w", serializeErr)
	}

	// 1. Vector Search
	rows, err := database.DB.WithContext(ctx).Raw(
		"SELECT file_id, chunk_id, text, distance FROM file_embeddings WHERE embedding MATCH ? AND k = ? ORDER BY distance ASC",
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
	seenChunks := make(map[string]bool)
	for rows.Next() {
		var res file.SearchResult
		if err := rows.Scan(&res.FileID, &res.ChunkID, &res.Text, &res.Distance); err != nil {
			slog.Error("sqlite-vec scan row failed", "error", err)
			return nil, fmt.Errorf("failed to scan search result row: %w", err)
		}
		results = append(results, res)
		seenChunks[res.ChunkID] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite-vec search rows iteration error: %w", err)
	}

	// 2. Keyword Search
	if len(keywords) > 0 {
		var conditions []string
		var args []interface{}
		for _, kw := range keywords {
			conditions = append(conditions, "text LIKE ?")
			args = append(args, "%"+kw+"%")
		}

		queryStr := fmt.Sprintf(
			"SELECT file_id, chunk_id, text FROM file_embeddings WHERE %s LIMIT ?",
			strings.Join(conditions, " OR "),
		)
		args = append(args, limit)

		kwRows, kwErr := database.DB.WithContext(ctx).Raw(queryStr, args...).Rows()
		if kwErr == nil {
			defer func() { _ = kwRows.Close() }()
			for kwRows.Next() {
				var res file.SearchResult
				if err := kwRows.Scan(&res.FileID, &res.ChunkID, &res.Text); err == nil {
					if !seenChunks[res.ChunkID] {
						// Assign a distance of 0.5 for keyword-only matches
						res.Distance = 0.5
						results = append(results, res)
						seenChunks[res.ChunkID] = true
					}
				}
			}
			if err := kwRows.Err(); err != nil {
				slog.Warn("sqlite-vec keyword rows iteration error", "error", err)
			}
		} else {
			slog.Warn("sqlite-vec keyword query failed", "error", kwErr)
		}
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
func (q *QdrantVectorDB) Insert(ctx context.Context, fileID string, chunkIndex int, text string, embedding []float32) error {
	base, err := url.Parse(q.URL)
	if err != nil {
		return fmt.Errorf("invalid qdrant url: %w", err)
	}

	targetURL := base.JoinPath("collections", q.Collection, "points").String()
	targetURL = targetURL + "?wait=true"

	chunkID := fmt.Sprintf("%s_%d", fileID, chunkIndex)
	pointUUID := toUUID(chunkID)
	reqBody := map[string]interface{}{
		"points": []map[string]interface{}{
			{
				"id":     pointUUID,
				"vector": embedding,
				"payload": map[string]interface{}{
					"file_id":  fileID,
					"chunk_id": chunkID,
					"text":     text,
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
		slog.Error("qdrant insert failed", "chunk_id", chunkID, "error", err)
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

	targetURL := base.JoinPath("collections", q.Collection, "points", "scroll").String()

	reqBody := map[string]interface{}{
		"filter": map[string]interface{}{
			"must": []map[string]interface{}{
				{
					"key": "file_id",
					"match": map[string]interface{}{
						"value": srcFileID,
					},
				},
			},
		},
		"with_vector":  true,
		"with_payload": true,
		"limit":        500, // support up to 500 chunks
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
		slog.Error("qdrant scroll points failed in Copy", "src_id", srcFileID, "error", err)
		return fmt.Errorf("failed to fetch source vectors: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		slog.Error("qdrant scroll returned non-OK status", "status", resp.Status, "response", string(body))
		return fmt.Errorf("qdrant scroll status: %s", resp.Status)
	}

	var respJSON struct {
		Result struct {
			Points []struct {
				Vector  []float32              `json:"vector"`
				Payload map[string]interface{} `json:"payload"`
			} `json:"points"`
		} `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&respJSON); err != nil {
		slog.Error("failed to decode qdrant scroll response", "error", err)
		return fmt.Errorf("failed to decode response: %w", err)
	}

	if len(respJSON.Result.Points) == 0 {
		slog.Warn("no vectors found for source file", "src_id", srcFileID)
		return nil
	}

	// Prepare batch upsert
	var upsertPoints []map[string]interface{}
	for _, p := range respJSON.Result.Points {
		oldChunkID, _ := p.Payload["chunk_id"].(string)
		oldText, _ := p.Payload["text"].(string)
		
		// Replace srcFileID with destFileID in chunk_id
		newChunkID := strings.Replace(oldChunkID, srcFileID, destFileID, 1)
		if newChunkID == "" || newChunkID == oldChunkID {
			newChunkID = fmt.Sprintf("%s_copied", destFileID)
		}
		newUUID := toUUID(newChunkID)

		upsertPoints = append(upsertPoints, map[string]interface{}{
			"id":     newUUID,
			"vector": p.Vector,
			"payload": map[string]interface{}{
				"file_id":  destFileID,
				"chunk_id": newChunkID,
				"text":     oldText,
			},
		})
	}

	upsertURL := base.JoinPath("collections", q.Collection, "points").String()
	upsertURL = upsertURL + "?wait=true"

	upsertBody := map[string]interface{}{
		"points": upsertPoints,
	}
	upsertBytes, err := json.Marshal(upsertBody)
	if err != nil {
		return err
	}

	uReq, err := http.NewRequestWithContext(ctx, http.MethodPut, upsertURL, bytes.NewReader(upsertBytes))
	if err != nil {
		return err
	}
	uReq.Header.Set("Content-Type", "application/json")

	uResp, err := q.Client.Do(uReq)
	if err != nil {
		slog.Error("qdrant copy insert failed", "dest_id", destFileID, "error", err)
		return fmt.Errorf("qdrant upsert failed: %w", err)
	}
	defer func() { _ = uResp.Body.Close() }()

	if uResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(uResp.Body)
		slog.Error("qdrant upsert returned non-OK status", "status", uResp.Status, "response", string(body))
		return fmt.Errorf("qdrant upsert status: %s", uResp.Status)
	}

	return nil
}

// Search finds similar vectors in Qdrant with hybrid keyword support.
func (q *QdrantVectorDB) Search(ctx context.Context, embedding []float32, keywords []string, limit int) ([]file.SearchResult, error) {
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
				FileID  string `json:"file_id"`
				ChunkID string `json:"chunk_id"`
				Text    string `json:"text"`
			} `json:"payload"`
		} `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&respJSON); err != nil {
		return nil, err
	}

	var results []file.SearchResult
	seenChunks := make(map[string]bool)
	for _, item := range respJSON.Result {
		results = append(results, file.SearchResult{
			FileID:   item.Payload.FileID,
			ChunkID:  item.Payload.ChunkID,
			Text:     item.Payload.Text,
			Distance: 1.0 - item.Score,
		})
		seenChunks[item.Payload.ChunkID] = true
	}

	// 2. Qdrant Keyword Scroll Search
	if len(keywords) > 0 {
		scrollURL := base.JoinPath("collections", q.Collection, "points", "scroll").String()

		var matches []map[string]interface{}
		for _, kw := range keywords {
			matches = append(matches, map[string]interface{}{
				"key": "text",
				"match": map[string]interface{}{
					"text": kw,
				},
			})
		}

		scrollReqBody := map[string]interface{}{
			"filter": map[string]interface{}{
				"should": matches,
			},
			"with_payload": true,
			"limit":        limit,
		}
		scrollBytes, err := json.Marshal(scrollReqBody)
		if err == nil {
			sReq, err := http.NewRequestWithContext(ctx, http.MethodPost, scrollURL, bytes.NewReader(scrollBytes))
			if err == nil {
				sReq.Header.Set("Content-Type", "application/json")
				sResp, err := q.Client.Do(sReq)
				if err == nil {
					defer func() { _ = sResp.Body.Close() }()
					if sResp.StatusCode == http.StatusOK {
						var scrollJSON struct {
							Result struct {
								Points []struct {
									Payload struct {
										FileID  string `json:"file_id"`
										ChunkID string `json:"chunk_id"`
										Text    string `json:"text"`
									} `json:"payload"`
								} `json:"points"`
							} `json:"result"`
						}
						if err := json.NewDecoder(sResp.Body).Decode(&scrollJSON); err == nil {
							for _, p := range scrollJSON.Result.Points {
								if !seenChunks[p.Payload.ChunkID] {
									results = append(results, file.SearchResult{
										FileID:   p.Payload.FileID,
										ChunkID:  p.Payload.ChunkID,
										Text:     p.Payload.Text,
										Distance: 0.5, // fallback distance
									})
									seenChunks[p.Payload.ChunkID] = true
								}
							}
						}
					}
				}
			}
		}
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

	reqBody := map[string]interface{}{
		"filter": map[string]interface{}{
			"must": []map[string]interface{}{
				{
					"key": "file_id",
					"match": map[string]interface{}{
						"value": fileID,
					},
				},
			},
		},
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
