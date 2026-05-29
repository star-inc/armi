package usecase

import (
	"context"
	"crypto/sha3"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/go-ego/gse"
	"github.com/rs/xid"
	"github.com/spf13/viper"
	"github.com/supersonictw/armi/internal/extractor"
	"github.com/supersonictw/armi/pkgs/contract"
	"github.com/supersonictw/armi/pkgs/file"
)


type FileUsecase struct {
	fileRepo     file.FileRepository
	storage      file.Storage
	embedder     file.Embedder
	vectorDB     file.VectorDB
	llm          file.LLM
	publisher    file.EventPublisher
	jobPublisher file.EmbeddingJobPublisher // nil when RabbitMQ is unavailable
	segmenter    *gse.Segmenter
}

func NewFileUsecase(
	fileRepo file.FileRepository,
	storage file.Storage,
	embedder file.Embedder,
	vectorDB file.VectorDB,
	llm file.LLM,
	publisher file.EventPublisher,
	jobPublisher file.EmbeddingJobPublisher,
) *FileUsecase {
	var segmenter *gse.Segmenter
	seg, err := gse.New("zh")
	if err != nil {
		slog.Warn("failed to initialize gse segmenter, word segmentation will be skipped", "error", err)
	} else {
		segmenter = &seg
		slog.Info("Initialized gse segmenter successfully")
	}

	return &FileUsecase{
		fileRepo:     fileRepo,
		storage:      storage,
		embedder:     embedder,
		vectorDB:     vectorDB,
		llm:          llm,
		publisher:    publisher,
		jobPublisher: jobPublisher,
		segmenter:    segmenter,
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
		Tags:        f.Tags,
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
	tags []string,
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
		Tags:        tags,
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

	// Vector Embedding — async via RabbitMQ when available, sync fallback otherwise.
	if uc.jobPublisher != nil && uc.jobPublisher.IsAvailable() {
		uc.dispatchEmbeddingJob(ctx, fileID, userID, sha3Hash, filename, contentType, globalCount)
	} else {
		if embedErr := uc.embedSync(ctx, fileID, userID, sha3Hash, filename, contentType, content, globalCount); embedErr != nil {
			slog.Error("embedding failed (sync), rolling back file record", "file_id", fileID, "error", embedErr)
			uc.publisher.PublishEvent(ctx, "embedding.failed", userID, map[string]interface{}{
				"file_id": fileID,
				"error":   embedErr.Error(),
			})
			// Roll back: delete the DB record so the upload appears as if it never happened.
			if delErr := uc.fileRepo.Delete(ctx, fileID); delErr != nil {
				slog.Error("failed to roll back file record after embedding error", "file_id", fileID, "error", delErr)
			}
			return nil, fmt.Errorf("embedding failed: %w", embedErr)
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

// dispatchEmbeddingJob enqueues an EmbeddingJob to the RabbitMQ work queue.
func (uc *FileUsecase) dispatchEmbeddingJob(
	ctx context.Context,
	fileID, userID, sha3Hash, filename, contentType string,
	globalCount int64,
) {
	job := contract.EmbeddingJob{
		JobID:       xid.New().String(),
		FileID:      fileID,
		UserID:      userID,
		StorageKey:  "sha3-256-" + sha3Hash,
		Filename:    filename,
		ContentType: contentType,
	}

	if globalCount > 0 {
		// Deduplication: look up the existing record's ID so the consumer can Copy.
		existing, err := uc.fileRepo.GetByHash(ctx, sha3Hash)
		if err == nil && existing != nil {
			job.IsCopy = true
			job.SrcFileID = existing.ID
		}
	}

	if err := uc.jobPublisher.PublishEmbeddingJob(ctx, job); err != nil {
		slog.Warn("failed to enqueue embedding job, falling back to sync embedding", "job_id", job.JobID, "error", err)
		uc.publisher.PublishEvent(ctx, "embedding.queue_error", userID, map[string]interface{}{
			"job_id": job.JobID,
			"error":  err.Error(),
		})
		// Sync fallback: content is no longer available here, skip silently.
		// The file is still saved; embedding can be retried later.
		return
	}

	slog.Info("embedding job enqueued", "job_id", job.JobID, "file_id", fileID)
	uc.publisher.PublishEvent(ctx, "embedding.queued", userID, map[string]interface{}{
		"job_id":  job.JobID,
		"file_id": fileID,
	})
}

// embedSync performs embedding in the same goroutine (used when RabbitMQ is unavailable).
// Returns an error if embedding fails; the caller is responsible for rolling back the upload.
func (uc *FileUsecase) embedSync(
	ctx context.Context,
	fileID, userID, sha3Hash, filename, contentType string,
	content []byte,
	globalCount int64,
) error {
	var vectorCopied bool
	if globalCount > 0 {
		existingRecord, err := uc.fileRepo.GetByHash(ctx, sha3Hash)
		if err == nil && existingRecord != nil {
			if copyErr := uc.vectorDB.Copy(ctx, existingRecord.ID, fileID); copyErr == nil {
				vectorCopied = true
				slog.Info("vector embedding copied (deduplicated, sync)", "src_id", existingRecord.ID, "dest_id", fileID)
			} else {
				slog.Warn("failed to copy existing vector, falling back to embedding generation (sync)", "error", copyErr)
			}
		}
	}

	if !vectorCopied {
		text, extractErr := extractor.ExtractText(content, filename)
		if extractErr != nil {
			return fmt.Errorf("text extraction failed: %w", extractErr)
		}
		if text == "" && uc.llm != nil {
			lowerFilename := strings.ToLower(filename)
			if strings.HasSuffix(lowerFilename, ".pdf") {
				slog.Info("Extracted text is empty, trying OCR on PDF pages", "filename", filename)
				ocrText, ocrErr := extractor.PerformOCRForPDF(ctx, content, uc.llm)
				if ocrErr == nil && ocrText != "" {
					text = ocrText
					slog.Info("Successfully extracted text via PDF OCR", "filename", filename, "text_len", len(text))
				} else if ocrErr != nil {
					slog.Warn("PDF OCR fallback failed", "filename", filename, "error", ocrErr)
				}
			} else if strings.HasSuffix(lowerFilename, ".pptx") || strings.HasSuffix(lowerFilename, ".ppt") {
				slog.Info("Extracted text is empty, trying OCR on PPTX embedded images", "filename", filename)
				ocrText, ocrErr := extractor.PerformOCRForPPTX(ctx, content, uc.llm)
				if ocrErr == nil && ocrText != "" {
					text = ocrText
					slog.Info("Successfully extracted text via PPTX OCR", "filename", filename, "text_len", len(text))
				} else if ocrErr != nil {
					slog.Warn("PPTX OCR fallback failed", "filename", filename, "error", ocrErr)
				}
			}
		}
		if text == "" {
			// No extractable text — not a fatal error, skip silently.
			return nil
		}

		chunkSize := viper.GetInt("chunk.size")
		if chunkSize <= 0 {
			chunkSize = 1000
		}
		chunkOverlap := viper.GetInt("chunk.overlap")
		if chunkOverlap < 0 {
			chunkOverlap = 200
		}

		chunks := extractor.SplitText(text, chunkSize, chunkOverlap)
		for i, chunk := range chunks {
			embeddingVal, embedErr := uc.embedder.Embed(ctx, chunk)
			if embedErr != nil {
				slog.Error("embedding provider error (sync)", "file_id", fileID, "chunk_index", i, "error", embedErr)
				return fmt.Errorf("embedding model error at chunk %d: %w", i, embedErr)
			}
			if err := uc.vectorDB.Insert(ctx, fileID, i, chunk, embeddingVal); err != nil {
				return fmt.Errorf("vector insert failed at chunk %d: %w", i, err)
			}
		}
	}
	return nil
}

func (uc *FileUsecase) List(ctx context.Context, userID string, tag string) ([]contract.FileResponse, error) {
	files, err := uc.fileRepo.ListByOwnerID(ctx, userID, tag)
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
	if err = uc.vectorDB.Delete(ctx, fileID); err != nil {
		slog.Error("failed to delete embedding from vector database", "file_id", fileID, "error", err)
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

	var keywords []string
	if uc.segmenter != nil {
		words := uc.segmenter.Cut(query, true)
		for _, w := range words {
			w = strings.TrimSpace(w)
			runes := []rune(w)
			if len(runes) > 1 {
				keywords = append(keywords, w)
			}
		}
		if len(keywords) > 0 {
			slog.Info("Segmented search query", "query", query, "keywords", keywords)
		}
	}

	type candidate struct {
		record      *file.FileRecord
		chunkID     string
		chunkText   string
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

		// Perform vector and keyword hybrid search
		searchResults, err := uc.vectorDB.Search(ctx, queryEmbedding, keywords, limit)
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
			existing, exists := mergedCandidates[res.ChunkID]
			if !exists || score > existing.score {
				mergedCandidates[res.ChunkID] = candidate{
					record:      r,
					chunkID:     res.ChunkID,
					chunkText:   res.Text,
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
			ChunkID:     c.chunkID,
			ChunkText:   c.chunkText,
		})
	}

	return responseItems, nil
}
