package usecase

import (
	"context"
	"crypto/sha3"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/go-ego/gse"
	"github.com/star-inc/armi/internal/embedding"
	"github.com/star-inc/armi/pkgs/contract"
	"github.com/star-inc/armi/pkgs/file"
	"github.com/rs/xid"
	"github.com/spf13/viper"
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
	seg, err := gse.NewEmbed("zh")
	if err != nil {
		slog.Warn("failed to initialize gse segmenter, word segmentation will be skipped", "error", err)
	} else {
		segmenter = &seg
		slog.Info("Initialized gse segmenter successfully (embed dictionary)")
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
		ID:              f.ID,
		Filename:        f.Filename,
		Description:     f.Description,
		Hash:            f.Hash,
		Size:            f.Size,
		ContentType:     f.ContentType,
		AuthorID:        f.AuthorID,
		GroupIDs:        f.GroupIDs,
		Tags:            f.Tags,
		EmbeddingStatus: f.EmbeddingStatus,
		CreatedAt:       f.CreatedAt,
		UpdatedAt:       f.UpdatedAt,
	}
}

func buildStorageKey(sha3Hash string) string {
	prefix := sha3Hash
	if len(sha3Hash) >= 2 {
		prefix = sha3Hash[:2]
	}
	return fmt.Sprintf("%s/%s", prefix, sha3Hash)
}

func (uc *FileUsecase) ensureGroupPermission(ctx context.Context, userID string, groupIDs []string, required file.GroupPermission) error {
	// If RBAC is disabled, bypass all access controls (delegated to Traefik/Caddy).
	if !viper.GetBool("auth.rbac.enabled") {
		return nil
	}
	if len(groupIDs) == 0 {
		return nil
	}
	for _, gid := range groupIDs {
		perm, ok, err := uc.fileRepo.GetGroupPermission(ctx, userID, gid)
		if err != nil {
			return err
		}
		if ok && perm >= required {
			return nil
		}
	}
	return file.ErrAccessDenied
}

func (uc *FileUsecase) authorizeFileAccess(ctx context.Context, userID string, record *file.FileRecord, required file.GroupPermission) error {
	// If RBAC is disabled, bypass all access controls (delegated to Traefik/Caddy).
	if !viper.GetBool("auth.rbac.enabled") {
		return nil
	}
	// File owner/author always has full access.
	if record.AuthorID == userID {
		return nil
	}
	// If the file does not belong to any groups and user is not the owner, access is denied.
	if len(record.GroupIDs) == 0 {
		return file.ErrAccessDenied
	}
	for _, gid := range record.GroupIDs {
		perm, ok, err := uc.fileRepo.GetGroupPermission(ctx, userID, gid)
		if err != nil {
			return err
		}
		if ok && perm >= required {
			return nil
		}
	}
	return file.ErrAccessDenied
}

// Upload handles progress reporting, conflict check, physical saving, DB entry and vector embedding.
func (uc *FileUsecase) Upload(
	ctx context.Context,
	userID string,
	filename string,
	description string,
	groupIDs []string,
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
	existingRecord, err := uc.fileRepo.GetByFilenameOrHash(ctx, userID, filename, sha3Hash)
	if err != nil {
		slog.Error("failed to check for file conflict", "error", err)
		return nil, err
	}
	if existingRecord != nil {
		slog.Warn("file conflict detected on upload", "filename", filename, "hash", sha3Hash, "user_id", userID, "conflicting_id", existingRecord.ID, "conflicting_hash", existingRecord.Hash)
		uc.publisher.PublishEvent(ctx, "file.conflict", userID, map[string]interface{}{
			"filename":         filename,
			"hash":             sha3Hash,
			"conflicting_id":   existingRecord.ID,
			"conflicting_hash": existingRecord.Hash,
			"reason":           "file with same name or content already exists for this user",
		})
		return nil, &file.ConflictError{
			ConflictingFileID:   existingRecord.ID,
			ConflictingFileHash: existingRecord.Hash,
		}
	}

	// Lock and increment the reference count in DB safely
	refCount, err := uc.fileRepo.GetOrCreateHashRecord(ctx, sha3Hash)
	if err != nil {
		slog.Error("failed to update hash reference count", "hash", sha3Hash, "error", err)
		return nil, err
	}

	globalCount := refCount - 1
	key := buildStorageKey(sha3Hash)

	if globalCount == 0 {
		// Save physical file
		err = uc.storage.Write(ctx, key, content)
		if err != nil {
			slog.Error("failed to write file to storage", "key", key, "error", err)
			_, _ = uc.fileRepo.DecrementHashRecord(ctx, sha3Hash)
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
		ID:              fileID,
		Filename:        filename,
		Description:     description,
		Hash:            sha3Hash,
		Size:            totalBytes,
		ContentType:     contentType,
		AuthorID:        userID,
		GroupIDs:        groupIDs,
		Tags:            tags,
		EmbeddingStatus: "pending",
	}

	if err := uc.ensureGroupPermission(ctx, userID, groupIDs, file.GroupPermissionWrite); err != nil {
		newRefCount, _ := uc.fileRepo.DecrementHashRecord(ctx, sha3Hash)
		if newRefCount == 0 {
			_ = uc.storage.Delete(ctx, key)
		}
		return nil, err
	}

	var outboxPayload string
	var useOutbox bool
	if uc.jobPublisher != nil && uc.jobPublisher.IsAvailable() {
		useOutbox = true
		job := contract.EmbeddingJob{
			JobID:       xid.New().String(),
			FileID:      fileID,
			UserID:      userID,
			StorageKey:  key,
			Filename:    filename,
			ContentType: contentType,
		}
		if globalCount > 0 {
			existing, err := uc.fileRepo.GetByHash(ctx, sha3Hash)
			if err == nil && existing != nil {
				job.IsCopy = true
				job.SrcFileID = existing.ID
			}
		}
		payloadBytes, _ := json.Marshal(job)
		outboxPayload = string(payloadBytes)
	}

	if useOutbox {
		err = uc.fileRepo.CreateWithOutbox(ctx, newRecord, outboxPayload)
	} else {
		err = uc.fileRepo.Create(ctx, newRecord)
	}

	if err != nil {
		slog.Error("failed to create file record", "error", err)
		newRefCount, _ := uc.fileRepo.DecrementHashRecord(ctx, sha3Hash)
		if newRefCount == 0 {
			_ = uc.storage.Delete(ctx, key)
		}
		uc.publisher.PublishEvent(ctx, "system.db_error", userID, map[string]interface{}{
			"operation": "create file record",
			"error":     err.Error(),
		})
		return nil, err
	}

	// Asynchronous dispatch (best-effort immediate dispatch, otherwise dispatcher catches it)
	if useOutbox {
		var job contract.EmbeddingJob
		_ = json.Unmarshal([]byte(outboxPayload), &job)
		
		slog.Info("embedding job queued via outbox", "job_id", job.JobID, "file_id", fileID)
		uc.publisher.PublishEvent(ctx, "embedding.queued", userID, map[string]interface{}{
			"job_id":  job.JobID,
			"file_id": fileID,
		})

		go func() {
			bgCtx := context.Background()
			if err := uc.jobPublisher.PublishEmbeddingJob(bgCtx, job); err == nil {
				if delErr := uc.fileRepo.DeleteOutboxJobByFileID(bgCtx, fileID); delErr != nil {
					slog.Warn("failed to delete outbox job after immediate dispatch", "file_id", fileID, "error", delErr)
				}
			}
		}()
	} else {
		// Vector Embedding sync fallback
		newRecord.EmbeddingStatus, err = uc.embedSync(
			ctx, fileID, userID, sha3Hash, filename, contentType, content, globalCount,
		)
		if err != nil {
			uc.rollbackUpload(ctx, fileID, userID, sha3Hash, globalCount, err)
			return nil, fmt.Errorf("sync embedding failed: %w", err)
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

// dispatchEmbeddingJob enqueues an EmbeddingJob to the RabbitMQ work queue directly.
func (uc *FileUsecase) dispatchEmbeddingJob(
	ctx context.Context,
	fileID, userID, sha3Hash, filename, contentType string,
	content []byte,
	globalCount int64,
) (string, error) {
	job := contract.EmbeddingJob{
		JobID:       xid.New().String(),
		FileID:      fileID,
		UserID:      userID,
		StorageKey:  buildStorageKey(sha3Hash),
		Filename:    filename,
		ContentType: contentType,
	}

	if globalCount > 0 {
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
		return uc.embedSync(ctx, fileID, userID, sha3Hash, filename, contentType, content, globalCount)
	}

	slog.Info("embedding job enqueued", "job_id", job.JobID, "file_id", fileID)
	uc.publisher.PublishEvent(ctx, "embedding.queued", userID, map[string]interface{}{
		"job_id":  job.JobID,
		"file_id": fileID,
	})
	return "pending", nil
}

func (uc *FileUsecase) rollbackUpload(
	ctx context.Context,
	fileID, userID, sha3Hash string,
	globalCount int64,
	embeddingErr error,
) {
	slog.Error("embedding failed, rolling back upload", "file_id", fileID, "error", embeddingErr)
	uc.publisher.PublishEvent(ctx, "embedding.failed", userID, map[string]interface{}{
		"file_id": fileID,
		"error":   embeddingErr.Error(),
	})
	if delErr := uc.fileRepo.Delete(ctx, fileID); delErr != nil {
		slog.Error("failed to roll back file record after embedding error", "file_id", fileID, "error", delErr)
	}
	if vecDelErr := uc.vectorDB.Delete(ctx, fileID); vecDelErr != nil {
		slog.Error("failed to roll back vectors after embedding error", "file_id", fileID, "error", vecDelErr)
	}
	newRefCount, _ := uc.fileRepo.DecrementHashRecord(ctx, sha3Hash)
	if newRefCount == 0 {
		key := buildStorageKey(sha3Hash)
		if storeDelErr := uc.storage.Delete(ctx, key); storeDelErr != nil {
			slog.Error("failed to roll back physical file after embedding error", "key", key, "error", storeDelErr)
		}
	}
}

// embedSync performs embedding in the same goroutine (used when RabbitMQ is unavailable).
// Returns the status ("completed" or "skipped") and an error if embedding fails.
func (uc *FileUsecase) embedSync(
	ctx context.Context,
	fileID, userID, sha3Hash, filename, contentType string,
	content []byte,
	globalCount int64,
) (string, error) {
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
		text, extractErr := uc.extractTextFromContent(ctx, content, filename)
		if extractErr != nil {
			return "", extractErr
		}
		if text == "" {
			// No extractable text — not a fatal error, skip silently.
			uc.updateEmbeddingStatus(ctx, fileID, "skipped")
			return "skipped", nil
		}

		if err := embedding.EmbedTextChunks(ctx, fileID, text, uc.embedder, uc.vectorDB); err != nil {
			slog.Error("embedding failed (sync)", "file_id", fileID, "error", err)
			return "", err
		}
	}
	uc.updateEmbeddingStatus(ctx, fileID, "completed")
	return "completed", nil
}

// extractTextFromContent processes raw bytes of a file to extract text, falling back to OCR if needed.
func (uc *FileUsecase) extractTextFromContent(ctx context.Context, content []byte, filename string) (string, error) {
	return embedding.ExtractTextWithOCR(ctx, content, filename, uc.llm)
}

func (uc *FileUsecase) updateEmbeddingStatus(ctx context.Context, fileID string, status string) {
	if err := uc.fileRepo.UpdateEmbeddingStatus(ctx, fileID, status); err != nil {
		slog.Error("failed to update embedding status in database", "file_id", fileID, "status", status, "error", err)
	}
}

// ExtractText fetches file content (verifying ownership) and extracts text using OCR fallback when necessary.
func (uc *FileUsecase) ExtractText(ctx context.Context, userID string, fileID string) (string, error) {
	data, filename, _, _, err := uc.Download(ctx, userID, fileID)
	if err != nil {
		return "", err
	}
	return uc.extractTextFromContent(ctx, data, filename)
}

func (uc *FileUsecase) ListPaginated(ctx context.Context, userID string, tag string, page int, pageSize int) (*contract.FileListResponse, error) {
	offset := (page - 1) * pageSize
	var (
		files []*file.FileRecord
		total int64
		err   error
	)
	// If RBAC is disabled, list all files globally (delegated to Traefik/Caddy).
	if viper.GetBool("auth.rbac.enabled") {
		files, total, err = uc.fileRepo.ListAccessible(ctx, userID, tag, file.GroupPermissionRead, pageSize, offset)
	} else {
		files, total, err = uc.fileRepo.List(ctx, tag, pageSize, offset)
	}
	if err != nil {
		slog.Error("failed to list user files with pagination", "user_id", userID, "page", page, "page_size", pageSize, "error", err)
		return nil, err
	}

	items := make([]contract.FileResponse, 0, len(files))
	for _, f := range files {
		items = append(items, toFileResponse(f))
	}

	totalPages := 0
	if total > 0 {
		totalPages = int((total + int64(pageSize) - 1) / int64(pageSize))
	}

	return &contract.FileListResponse{
		Items:      items,
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
	}, nil
}

func (uc *FileUsecase) Download(ctx context.Context, userID string, fileID string) ([]byte, string, string, int64, error) {
	record, err := uc.fileRepo.GetByID(ctx, fileID)
	if err != nil {
		return nil, "", "", 0, err
	}
	if record == nil {
		return nil, "", "", 0, file.ErrFileNotFound
	}
	if err := uc.authorizeFileAccess(ctx, userID, record, file.GroupPermissionRead); err != nil {
		return nil, "", "", 0, err
	}

	key := buildStorageKey(record.Hash)
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

func (uc *FileUsecase) GetMetadata(ctx context.Context, userID string, fileID string) (*contract.FileResponse, *contract.StorageMetadataResponse, error) {
	record, err := uc.fileRepo.GetByID(ctx, fileID)
	if err != nil {
		return nil, nil, err
	}
	if record == nil {
		return nil, nil, file.ErrFileNotFound
	}
	if err := uc.authorizeFileAccess(ctx, userID, record, file.GroupPermissionRead); err != nil {
		return nil, nil, err
	}

	key := buildStorageKey(record.Hash)
	stat, err := uc.storage.Stat(ctx, key)
	opMetadata := &contract.StorageMetadataResponse{}
	if err != nil {
		slog.Warn("failed to fetch storage metadata stat", "key", key, "error", err)
		opMetadata.Error = "storage status unavailable"
	} else {
		opMetadata.ContentLength = stat.ContentLength
		opMetadata.LastModified = stat.LastModified
	}

	uc.publisher.PublishEvent(ctx, "file.metadata_accessed", userID, map[string]interface{}{
		"file_id":  record.ID,
		"filename": record.Filename,
	})

	resp := toFileResponse(record)
	return &resp, opMetadata, nil
}

func (uc *FileUsecase) UpdateMetadata(ctx context.Context, userID string, fileID string, filename *string, description *string, groupIDs []string, tags []string) (*contract.FileResponse, error) {
	record, err := uc.fileRepo.GetByID(ctx, fileID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, file.ErrFileNotFound
	}
	if err := uc.authorizeFileAccess(ctx, userID, record, file.GroupPermissionWrite); err != nil {
		return nil, err
	}
	if filename != nil {
		newName := strings.TrimSpace(*filename)
		if newName == "" {
			return nil, errors.New("filename cannot be empty")
		}
		record.Filename = newName
	}
	if description != nil {
		record.Description = strings.TrimSpace(*description)
	}
	if groupIDs != nil {
		cleanedGroupIDs := make([]string, 0, len(groupIDs))
		for _, gid := range groupIDs {
			trimmed := strings.TrimSpace(gid)
			if trimmed != "" {
				cleanedGroupIDs = append(cleanedGroupIDs, trimmed)
			}
		}
		if err := uc.authorizeFileAccess(ctx, userID, record, file.GroupPermissionManage); err != nil {
			return nil, err
		}
		if err := uc.ensureGroupPermission(ctx, userID, cleanedGroupIDs, file.GroupPermissionManage); err != nil {
			return nil, err
		}
		record.GroupIDs = cleanedGroupIDs
	}
	if tags != nil {
		cleaned := make([]string, 0, len(tags))
		for _, t := range tags {
			trimmed := strings.TrimSpace(t)
			if trimmed != "" {
				cleaned = append(cleaned, trimmed)
			}
		}
		record.Tags = cleaned
	}

	if err := uc.fileRepo.Update(ctx, record); err != nil {
		return nil, err
	}

	uc.publisher.PublishEvent(ctx, "file.metadata_updated", userID, map[string]interface{}{
		"file_id":     record.ID,
		"filename":    record.Filename,
		"description": record.Description,
		"tags":        record.Tags,
	})

	resp := toFileResponse(record)
	return &resp, nil
}

func (uc *FileUsecase) Delete(ctx context.Context, userID string, fileID string) (bool, error) {
	record, err := uc.fileRepo.GetByID(ctx, fileID)
	if err != nil {
		return false, err
	}
	if record == nil {
		return false, file.ErrFileNotFound
	}
	if err := uc.authorizeFileAccess(ctx, userID, record, file.GroupPermissionManage); err != nil {
		return false, err
	}

	// Decrement the reference count safely
	newRefCount, err := uc.fileRepo.DecrementHashRecord(ctx, record.Hash)
	if err != nil {
		return false, fmt.Errorf("decrement hash record: %w", err)
	}

	deletePhysical := (newRefCount == 0)
	key := buildStorageKey(record.Hash)

	// Create cleanup job and delete file record atomically in a transaction
	cleanupJob := &file.CleanupJob{
		ID:             xid.New().String(),
		FileID:         fileID,
		Hash:           record.Hash,
		StorageKey:     key,
		DeletePhysical: deletePhysical,
		Status:         "pending",
	}

	if err := uc.fileRepo.DeleteWithCleanup(ctx, fileID, cleanupJob); err != nil {
		// Compensate: re-increment reference count if deletion registration fails
		_, _ = uc.fileRepo.GetOrCreateHashRecord(ctx, record.Hash)
		uc.publisher.PublishEvent(ctx, "system.db_error", userID, map[string]interface{}{
			"operation": "delete file record",
			"error":     err.Error(),
		})
		return false, fmt.Errorf("failed to register file deletion: %w", err)
	}

	// Trigger immediate background cleanup (best-effort)
	go func() {
		bgCtx := context.Background()
		vecDelErr := uc.vectorDB.Delete(bgCtx, fileID)
		var storeDelErr error
		if deletePhysical {
			// Double check active ref count
			c, cErr := uc.fileRepo.CountByHash(bgCtx, record.Hash)
			if cErr == nil && c == 0 {
				storeDelErr = uc.storage.Delete(bgCtx, key)
			}
		}
		if vecDelErr == nil && storeDelErr == nil {
			_ = uc.fileRepo.DeleteCleanupJob(bgCtx, cleanupJob.ID)
		}
	}()

	uc.publisher.PublishEvent(ctx, "file.deleted", userID, map[string]interface{}{
		"file_id":          record.ID,
		"filename":         record.Filename,
		"hash":             record.Hash,
		"physical_deleted": deletePhysical,
	})

	return deletePhysical, nil
}

func (uc *FileUsecase) restoreVector(ctx context.Context, record *file.FileRecord, content []byte) error {
	if record.EmbeddingStatus != "completed" {
		return nil
	}
	text, err := uc.extractTextFromContent(ctx, content, record.Filename)
	if err != nil {
		return fmt.Errorf("restore vector text extraction: %w", err)
	}
	if text == "" {
		return nil
	}
	if err := embedding.EmbedTextChunks(ctx, record.ID, text, uc.embedder, uc.vectorDB); err != nil {
		return fmt.Errorf("restore vector: %w", err)
	}
	return nil
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

	nlpEnabledGlobal := viper.GetBool("llm.query_expansion.enabled")
	if nlpExpansion && nlpEnabledGlobal && uc.llm != nil {
		maxLimit := viper.GetInt("llm.query_expansion.max_limit")
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
	fileCache := make(map[string]*file.FileRecord)

	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, q := range targetQueries {
		wg.Add(1)
		go func(q string) {
			defer wg.Done()

			// Generate embedding for query text
			queryEmbedding, err := uc.embedder.Embed(ctx, q)
			if err != nil {
				slog.Error("failed to generate search query embedding", "query", q, "error", err)
				return
			}

			// Perform vector and keyword hybrid search
			searchResults, err := uc.vectorDB.Search(ctx, queryEmbedding, keywords, limit)
			if err != nil {
				slog.Error("vector database search failed", "query", q, "error", err)
				return
			}

			// Filter by ownership and populate merge candidates
			for _, res := range searchResults {
				mu.Lock()
				r, exists := fileCache[res.FileID]
				mu.Unlock()

				if !exists {
					var err error
					r, err = uc.fileRepo.GetByID(ctx, res.FileID)
					mu.Lock()
					if err != nil {
						fileCache[res.FileID] = nil
						mu.Unlock()
						continue
					}
					fileCache[res.FileID] = r
					mu.Unlock()
				}
				if r == nil {
					continue
				}
				if err := uc.authorizeFileAccess(ctx, userID, r, file.GroupPermissionRead); err != nil {
					continue
				}

				score := 1.0 - res.Distance
				mu.Lock()
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
				mu.Unlock()
			}
		}(q)
	}
	wg.Wait()

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
			FileResponse: toFileResponse(c.record),
			Distance:     c.distance,
			Score:        c.score,
			SourceQuery:  c.sourceQuery,
			ChunkID:      c.chunkID,
			ChunkText:    c.chunkText,
		})
	}

	return responseItems, nil
}
