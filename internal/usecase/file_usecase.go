package usecase

import (
	"context"
	"crypto/sha3"
	"encoding/hex"
	"errors"
	"log/slog"
	"sort"

	"github.com/rs/xid"
	"github.com/spf13/viper"
	"github.com/supersonictw/armi/internal/extractor"
	"github.com/supersonictw/armi/pkgs/contract"
	"github.com/supersonictw/armi/pkgs/file"
)

type FileUsecase struct {
	fileRepo  file.FileRepository
	storage   file.Storage
	embedder  file.Embedder
	vectorDB  file.VectorDB
	llm       file.LLM
	publisher file.EventPublisher
}

func NewFileUsecase(
	fileRepo file.FileRepository,
	storage file.Storage,
	embedder file.Embedder,
	vectorDB file.VectorDB,
	llm file.LLM,
	publisher file.EventPublisher,
) *FileUsecase {
	return &FileUsecase{
		fileRepo:  fileRepo,
		storage:   storage,
		embedder:  embedder,
		vectorDB:  vectorDB,
		llm:       llm,
		publisher: publisher,
	}
}

func toFileResponse(f *file.FileRecord) contract.FileResponse {
	return contract.FileResponse{
		ID:          f.ID,
		Filename:    f.Filename,
		Hash:        f.Hash,
		Size:        f.Size,
		ContentType: f.ContentType,
		OwnerID:     f.OwnerID,
		CreatedAt:   f.CreatedAt,
		UpdatedAt:   f.UpdatedAt,
	}
}

// Upload handles progress reporting, conflict check, physical saving, DB entry and vector embedding.
func (uc *FileUsecase) Upload(
	ctx context.Context,
	userID string,
	filename string,
	contentType string,
	content []byte,
	transferID string,
) (*contract.FileResponse, error) {
	totalBytes := int64(len(content))

	// Calculate SHA3-256 hash
	hasher := sha3.New256()
	hasher.Write(content)
	sha3Hash := hex.EncodeToString(hasher.Sum(nil))

	// Check if this file already exists for this user (conflict)
	conflictCount, err := uc.fileRepo.CountByFilenameOrHash(ctx, userID, filename, sha3Hash)
	if err != nil {
		slog.Error("failed to check for file conflict", "error", err)
		return nil, err
	}
	if conflictCount > 0 {
		slog.Warn("file conflict detected on upload", "filename", filename, "hash", sha3Hash, "user_id", userID)
		uc.publisher.PublishEvent(ctx, "file.conflict", userID, map[string]interface{}{
			"filename": filename,
			"hash":     sha3Hash,
			"reason":   "file with same name or content already exists for this user",
		})
		return nil, errors.New("file conflict: identical file or filename already exists")
	}

	// Check global reference count for physical file deduplication
	globalCount, err := uc.fileRepo.CountByHash(ctx, sha3Hash)
	if err != nil {
		slog.Error("failed to perform global file lookup", "error", err)
		return nil, err
	}

	key := "sha3-256-" + sha3Hash
	if globalCount == 0 {
		// Save physical file
		err = uc.storage.Write(ctx, key, content)
		if err != nil {
			slog.Error("failed to write file to storage", "key", key, "error", err)
			uc.publisher.PublishEvent(ctx, "system.storage_error", userID, map[string]interface{}{
				"operation": "write",
				"path":      key,
				"error":     err.Error(),
			})
			return nil, err
		}
		uc.publisher.PublishEvent(ctx, "storage.physical_created", userID, map[string]interface{}{
			"hash": sha3Hash,
			"size": totalBytes,
		})
	}

	// Insert database record
	fileID := xid.New().String()
	newRecord := &file.FileRecord{
		ID:          fileID,
		Filename:    filename,
		Hash:        sha3Hash,
		Size:        totalBytes,
		ContentType: contentType,
		OwnerID:     userID,
	}

	err = uc.fileRepo.Create(ctx, newRecord)
	if err != nil {
		slog.Error("failed to create file record", "error", err)
		uc.publisher.PublishEvent(ctx, "system.db_error", userID, map[string]interface{}{
			"operation": "create file record",
			"error":     err.Error(),
		})
		return nil, err
	}

	// Vector Embedding (deduplicated or new)
	var vectorCopied bool
	if globalCount > 0 {
		// Find existing file record ID with same hash, copy embedding
		existingRecord, err := uc.fileRepo.GetByHash(ctx, sha3Hash)
		if err == nil && existingRecord != nil {
			err = uc.vectorDB.Copy(ctx, existingRecord.ID, fileID)
			if err == nil {
				vectorCopied = true
				slog.Info("vector embedding copied successfully (deduplicated)", "src_id", existingRecord.ID, "dest_id", fileID)
			} else {
				slog.Warn("failed to copy existing vector, falling back to embedding generation", "error", err)
			}
		}
	}

	if !vectorCopied {
		// New file vector flow
		text, extractErr := extractor.ExtractText(content, filename)
		if extractErr != nil {
			slog.Warn("failed to extract text from file, skipping vector generation", "error", extractErr)
		} else if text != "" {
			embeddingVal, embedErr := uc.embedder.Embed(ctx, text)
			if embedErr != nil {
				slog.Warn("failed to generate embedding from extracted text, skipping vector insert", "error", embedErr)
			} else {
				err = uc.vectorDB.Insert(ctx, fileID, embeddingVal)
				if err != nil {
					slog.Warn("failed to insert vector into vector database", "error", err)
				}
			}
		}
	}

	uc.publisher.PublishEvent(ctx, "file.uploaded", userID, map[string]interface{}{
		"file_id":      fileID,
		"filename":     filename,
		"size":         totalBytes,
		"content_type": contentType,
		"hash":         sha3Hash,
	})

	resp := toFileResponse(newRecord)
	return &resp, nil
}

func (uc *FileUsecase) List(ctx context.Context, userID string) ([]contract.FileResponse, error) {
	files, err := uc.fileRepo.ListByOwnerID(ctx, userID)
	if err != nil {
		slog.Error("failed to list user files", "user_id", userID, "error", err)
		return nil, err
	}

	results := make([]contract.FileResponse, len(files))
	for i, f := range files {
		results[i] = toFileResponse(f)
	}
	return results, nil
}

func (uc *FileUsecase) Download(ctx context.Context, userID string, fileID string) ([]byte, string, string, int64, error) {
	record, err := uc.fileRepo.GetByID(ctx, fileID)
	if err != nil {
		return nil, "", "", 0, err
	}
	if record == nil {
		return nil, "", "", 0, errors.New("file not found")
	}

	// Verify ownership
	if record.OwnerID != userID {
		return nil, "", "", 0, errors.New("access denied")
	}

	key := "sha3-256-" + record.Hash
	data, err := uc.storage.Read(ctx, key)
	if err != nil {
		slog.Error("failed to read file from storage", "key", key, "error", err)
		uc.publisher.PublishEvent(ctx, "system.storage_error", userID, map[string]interface{}{
			"operation": "read",
			"path":      key,
			"error":     err.Error(),
		})
		return nil, "", "", 0, err
	}

	uc.publisher.PublishEvent(ctx, "file.downloaded", userID, map[string]interface{}{
		"file_id":  record.ID,
		"filename": record.Filename,
		"size":     record.Size,
	})

	return data, record.Filename, record.ContentType, record.Size, nil
}

func (uc *FileUsecase) GetMetadata(ctx context.Context, userID string, fileID string) (*contract.FileResponse, map[string]interface{}, error) {
	record, err := uc.fileRepo.GetByID(ctx, fileID)
	if err != nil {
		return nil, nil, err
	}
	if record == nil {
		return nil, nil, errors.New("file not found")
	}

	// Verify ownership
	if record.OwnerID != userID {
		return nil, nil, errors.New("access denied")
	}

	key := "sha3-256-" + record.Hash
	stat, err := uc.storage.Stat(ctx, key)
	opMetadata := make(map[string]interface{})
	if err != nil {
		slog.Warn("failed to fetch storage metadata stat", "key", key, "error", err)
		opMetadata["error"] = "storage status unavailable"
	} else {
		opMetadata["content_length"] = stat.ContentLength
		opMetadata["last_modified"] = stat.LastModified
	}

	uc.publisher.PublishEvent(ctx, "file.metadata_accessed", userID, map[string]interface{}{
		"file_id":  record.ID,
		"filename": record.Filename,
	})

	resp := toFileResponse(record)
	return &resp, opMetadata, nil
}

func (uc *FileUsecase) Delete(ctx context.Context, userID string, fileID string) (bool, error) {
	record, err := uc.fileRepo.GetByID(ctx, fileID)
	if err != nil {
		return false, err
	}
	if record == nil {
		return false, errors.New("file not found")
	}

	// Verify ownership
	if record.OwnerID != userID {
		return false, errors.New("access denied")
	}

	// Delete DB Record
	err = uc.fileRepo.Delete(ctx, fileID)
	if err != nil {
		slog.Error("failed to delete file record from repository", "file_id", fileID, "error", err)
		uc.publisher.PublishEvent(ctx, "system.db_error", userID, map[string]interface{}{
			"operation": "delete file record",
			"error":     err.Error(),
		})
		return false, err
	}

	// Delete Vector point
	err = uc.vectorDB.Delete(ctx, fileID)
	if err != nil {
		slog.Warn("failed to delete embedding from vector database", "file_id", fileID, "error", err)
	}

	// Check global reference count
	otherCount, err := uc.fileRepo.CountByHash(ctx, record.Hash)
	if err != nil {
		slog.Error("failed to check other references for hash", "hash", record.Hash, "error", err)
	}

	var physicalDeleted bool
	if otherCount == 0 {
		key := "sha3-256-" + record.Hash
		err = uc.storage.Delete(ctx, key)
		if err != nil {
			slog.Error("failed to delete physical file", "key", key, "error", err)
			uc.publisher.PublishEvent(ctx, "system.storage_error", userID, map[string]interface{}{
				"operation": "delete physical file",
				"path":      key,
				"error":     err.Error(),
			})
		} else {
			physicalDeleted = true
			uc.publisher.PublishEvent(ctx, "storage.physical_deleted", userID, map[string]interface{}{
				"hash": record.Hash,
			})
		}
	}

	uc.publisher.PublishEvent(ctx, "file.deleted", userID, map[string]interface{}{
		"file_id":          record.ID,
		"filename":         record.Filename,
		"hash":             record.Hash,
		"physical_deleted": physicalDeleted,
	})

	return physicalDeleted, nil
}

func (uc *FileUsecase) Search(
	ctx context.Context,
	userID string,
	query string,
	limit int,
	nlpExpansion bool,
	expansionNum int,
) ([]contract.SearchResponseItem, error) {
	if query == "" {
		return nil, errors.New("query text is required")
	}

	var targetQueries []string
	targetQueries = append(targetQueries, query)

	nlpEnabledGlobal := viper.GetBool("search.nlp_expansion.enabled")
	if nlpExpansion && nlpEnabledGlobal && uc.llm != nil {
		maxLimit := viper.GetInt("search.nlp_expansion.max_limit")
		if maxLimit <= 0 {
			maxLimit = 10
		}
		if expansionNum > maxLimit {
			expansionNum = maxLimit
		}

		expanded, err := uc.llm.GenerateQueries(ctx, query, expansionNum)
		if err != nil {
			slog.Warn("failed to generate NLP expanded queries, falling back to original query only", "error", err)
		} else {
			targetQueries = append(targetQueries, expanded...)
		}
	}

	type candidate struct {
		record      *file.FileRecord
		distance    float32
		score       float32
		sourceQuery string
	}

	mergedCandidates := make(map[string]candidate)

	for _, q := range targetQueries {
		// Generate embedding for query text
		queryEmbedding, err := uc.embedder.Embed(ctx, q)
		if err != nil {
			slog.Error("failed to generate search query embedding", "query", q, "error", err)
			continue
		}

		// Perform vector search
		searchResults, err := uc.vectorDB.Search(ctx, queryEmbedding, limit)
		if err != nil {
			slog.Error("vector database search failed", "query", q, "error", err)
			continue
		}

		// Filter by ownership and populate merge candidates
		for _, res := range searchResults {
			r, err := uc.fileRepo.GetByID(ctx, res.FileID)
			if err != nil || r == nil || r.OwnerID != userID {
				continue
			}

			score := 1.0 - res.Distance
			existing, exists := mergedCandidates[res.FileID]
			if !exists || score > existing.score {
				mergedCandidates[res.FileID] = candidate{
					record:      r,
					distance:    res.Distance,
					score:       score,
					sourceQuery: q,
				}
			}
		}
	}

	var candidatesList []candidate
	for _, c := range mergedCandidates {
		candidatesList = append(candidatesList, c)
	}

	// Sort candidates by score descending
	sort.Slice(candidatesList, func(i, j int) bool {
		return candidatesList[i].score > candidatesList[j].score
	})

	// Slice to requested limit
	if len(candidatesList) > limit {
		candidatesList = candidatesList[:limit]
	}

	// Sort and format response items
	var responseItems []contract.SearchResponseItem
	for _, c := range candidatesList {
		responseItems = append(responseItems, contract.SearchResponseItem{
			FileResponse: contract.FileResponse{
				ID:          c.record.ID,
				Filename:    c.record.Filename,
				Hash:        c.record.Hash,
				Size:        c.record.Size,
				ContentType: c.record.ContentType,
				OwnerID:     c.record.OwnerID,
				CreatedAt:   c.record.CreatedAt,
				UpdatedAt:   c.record.UpdatedAt,
			},
			Distance:    c.distance,
			Score:       c.score,
			SourceQuery: c.sourceQuery,
		})
	}

	return responseItems, nil
}
