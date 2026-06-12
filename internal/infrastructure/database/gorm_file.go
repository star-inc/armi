package database

import (
	"context"
	"errors"
	"time"

	"github.com/star-inc/armi/pkgs/file"
	"github.com/rs/xid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func getOrCreateTag(ctx context.Context, db *gorm.DB, tagName string) (gormTag, error) {
	// Atomic upsert: insert if missing, no-op on unique(name) conflict.
	seed := gormTag{
		ID:   xid.New().String(),
		Name: tagName,
	}
	if err := db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "name"}},
			DoNothing: true,
		}).
		Create(&seed).Error; err != nil {
		return gormTag{}, err
	}

	// Resolve canonical row regardless of whether insert happened or conflicted.
	var gt gormTag
	if err := db.WithContext(ctx).Where("name = ?", tagName).First(&gt).Error; err != nil {
		return gormTag{}, err
	}
	return gt, nil
}

// GormFileRepository implements file.FileRepository interface.
type GormFileRepository struct {
	db *gorm.DB
}

// NewGormFileRepository constructs a new GormFileRepository.
func NewGormFileRepository(db *gorm.DB) file.FileRepository {
	return &GormFileRepository{db: db}
}

// toDomainFile maps internal database schema gormFileRecord to domain FileRecord.
func toDomainFile(g *gormFileRecord) *file.FileRecord {
	if g == nil {
		return nil
	}
	tags := make([]string, len(g.Tags))
	for i, t := range g.Tags {
		tags[i] = t.Name
	}
	return &file.FileRecord{
		ID:              g.ID,
		Filename:        g.Filename,
		Description:     g.Description,
		Hash:            g.Hash,
		Size:            g.Size,
		ContentType:     g.ContentType,
		AuthorID:        g.AuthorID,
		Tags:            tags,
		EmbeddingStatus: g.EmbeddingStatus,
		CreatedAt:       g.CreatedAt,
		UpdatedAt:       g.UpdatedAt,
	}
}

func (r *GormFileRepository) getGroupIDsByFileID(ctx context.Context, fileID string) ([]string, error) {
	var rows []gormFileGroupFile
	if err := r.db.WithContext(ctx).Where("file_id = ?", fileID).Find(&rows).Error; err != nil {
		return nil, err
	}
	groupIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		groupIDs = append(groupIDs, row.FileGroupID)
	}
	return groupIDs, nil
}

// Create inserts a FileRecord entity into database.
func (r *GormFileRepository) Create(ctx context.Context, f *file.FileRecord) error {
	var gormTags []gormTag
	for _, tagName := range f.Tags {
		gt, err := getOrCreateTag(ctx, r.db, tagName)
		if err != nil {
			return err
		}
		gormTags = append(gormTags, gt)
	}

	status := f.EmbeddingStatus
	if status == "" {
		status = "pending"
	}
	gf := &gormFileRecord{
		ID:              f.ID,
		Filename:        f.Filename,
		Description:     f.Description,
		Hash:            f.Hash,
		Size:            f.Size,
		ContentType:     f.ContentType,
		AuthorID:        f.AuthorID,
		Tags:            gormTags,
		EmbeddingStatus: status,
	}
	err := r.db.WithContext(ctx).Create(gf).Error
	if err != nil {
		return err
	}
	if err := r.ReplaceFileGroups(ctx, f.ID, f.GroupIDs); err != nil {
		return err
	}
	f.CreatedAt = gf.CreatedAt
	f.UpdatedAt = gf.UpdatedAt
	f.EmbeddingStatus = gf.EmbeddingStatus
	return nil
}

// GetByID finds a FileRecord by ID. Returns nil, nil if not found.
func (r *GormFileRepository) GetByID(ctx context.Context, id string) (*file.FileRecord, error) {
	var gf gormFileRecord
	err := r.db.WithContext(ctx).Preload("Tags").First(&gf, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	fd := toDomainFile(&gf)
	gids, err := r.getGroupIDsByFileID(ctx, fd.ID)
	if err != nil {
		return nil, err
	}
	fd.GroupIDs = gids
	return fd, nil
}

// GetByHash finds a FileRecord by Hash. Returns nil, nil if not found.
func (r *GormFileRepository) GetByHash(ctx context.Context, hash string) (*file.FileRecord, error) {
	var gf gormFileRecord
	err := r.db.WithContext(ctx).Preload("Tags").First(&gf, "hash = ?", hash).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	fd := toDomainFile(&gf)
	gids, err := r.getGroupIDsByFileID(ctx, fd.ID)
	if err != nil {
		return nil, err
	}
	fd.GroupIDs = gids
	return fd, nil
}

// Update updates mutable metadata fields (filename, description, tags) for an existing file record.
func (r *GormFileRepository) Update(ctx context.Context, f *file.FileRecord) error {
	var gf gormFileRecord
	if err := r.db.WithContext(ctx).Preload("Tags").First(&gf, "id = ?", f.ID).Error; err != nil {
		return err
	}

	updates := map[string]interface{}{
		"filename":    f.Filename,
		"description": f.Description,
	}
	if f.EmbeddingStatus != "" {
		updates["embedding_status"] = f.EmbeddingStatus
	}
	if err := r.db.WithContext(ctx).Model(&gf).Updates(updates).Error; err != nil {
		return err
	}

	var gormTags []gormTag
	for _, tagName := range f.Tags {
		gt, err := getOrCreateTag(ctx, r.db, tagName)
		if err != nil {
			return err
		}
		gormTags = append(gormTags, gt)
	}

	if err := r.db.WithContext(ctx).Model(&gf).Association("Tags").Replace(gormTags); err != nil {
		return err
	}
	if err := r.ReplaceFileGroups(ctx, f.ID, f.GroupIDs); err != nil {
		return err
	}

	refreshed, err := r.GetByID(ctx, f.ID)
	if err != nil {
		return err
	}
	if refreshed != nil {
		f.Filename = refreshed.Filename
		f.Description = refreshed.Description
		f.GroupIDs = refreshed.GroupIDs
		f.Tags = refreshed.Tags
		f.UpdatedAt = refreshed.UpdatedAt
		f.CreatedAt = refreshed.CreatedAt
	}
	return nil
}

// UpdateEmbeddingStatus updates only the embedding status. Replaying the same
// event is therefore idempotent and does not rewrite tags or group mappings.
// It enforces monotonic status transitions:
// - 'processing' is only allowed if current status is 'pending'
// - 'completed', 'failed', and 'skipped' are allowed if current status is 'pending' or 'processing'.
func (r *GormFileRepository) UpdateEmbeddingStatus(ctx context.Context, id string, status string) error {
	query := r.db.WithContext(ctx).Model(&gormFileRecord{})
	switch status {
	case "processing":
		query = query.Where("id = ? AND embedding_status = ?", id, "pending")
	case "completed", "failed", "skipped":
		query = query.Where("id = ? AND embedding_status IN (?, ?)", id, "pending", "processing")
	default:
		query = query.Where("id = ? AND embedding_status <> ?", id, status)
	}
	return query.Update("embedding_status", status).Error
}

// List fetches file records with pagination, without filtering by author.
func (r *GormFileRepository) List(ctx context.Context, tag string, limit int, offset int) ([]*file.FileRecord, int64, error) {
	return r.list(ctx, nil, tag, limit, offset)
}

// ListAccessible filters by group membership before count, limit, and offset are
// applied so pagination metadata describes the rows visible to the caller.
func (r *GormFileRepository) ListAccessible(
	ctx context.Context,
	userID string,
	tag string,
	required file.GroupPermission,
	limit int,
	offset int,
) ([]*file.FileRecord, int64, error) {
	permissionFilter := `(
		NOT EXISTS (
			SELECT 1 FROM file_group_files fgf
			WHERE fgf.file_id = file_records.id
		)
		OR EXISTS (
			SELECT 1
			FROM file_group_files fgf
			JOIN file_group_members fgm
				ON fgm.file_group_id = fgf.file_group_id
			WHERE fgf.file_id = file_records.id
				AND fgm.user_id = ?
				AND fgm.permission >= ?
		)
	)`
	return r.listWithFilter(ctx, nil, tag, limit, offset, permissionFilter, userID, int(required))
}

// ListByAuthorID fetches file records for an author with pagination.
func (r *GormFileRepository) ListByAuthorID(ctx context.Context, authorID string, tag string, limit int, offset int) ([]*file.FileRecord, int64, error) {
	return r.list(ctx, &authorID, tag, limit, offset)
}

func (r *GormFileRepository) list(ctx context.Context, authorID *string, tag string, limit int, offset int) ([]*file.FileRecord, int64, error) {
	return r.listWithFilter(ctx, authorID, tag, limit, offset, "")
}

func (r *GormFileRepository) listWithFilter(
	ctx context.Context,
	authorID *string,
	tag string,
	limit int,
	offset int,
	extraFilter string,
	extraArgs ...interface{},
) ([]*file.FileRecord, int64, error) {
	var (
		gfs   []gormFileRecord
		total int64
	)

	query := r.db.WithContext(ctx).Model(&gormFileRecord{})
	if authorID != nil {
		query = query.Where("author_id = ?", *authorID)
	}
	if tag != "" {
		query = query.Where("id IN (SELECT gorm_file_record_id FROM file_tags JOIN tags ON tags.id = file_tags.gorm_tag_id WHERE tags.name = ?)", tag)
	}
	if extraFilter != "" {
		query = query.Where(extraFilter, extraArgs...)
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return []*file.FileRecord{}, 0, nil
	}

	dataQuery := r.db.WithContext(ctx).Preload("Tags").Order("created_at DESC")
	if authorID != nil {
		dataQuery = dataQuery.Where("author_id = ?", *authorID)
	}
	if tag != "" {
		dataQuery = dataQuery.Where("id IN (SELECT gorm_file_record_id FROM file_tags JOIN tags ON tags.id = file_tags.gorm_tag_id WHERE tags.name = ?)", tag)
	}
	if extraFilter != "" {
		dataQuery = dataQuery.Where(extraFilter, extraArgs...)
	}
	if err := dataQuery.Limit(limit).Offset(offset).Find(&gfs).Error; err != nil {
		return nil, 0, err
	}

	results := make([]*file.FileRecord, len(gfs))
	for i := range gfs {
		fd := toDomainFile(&gfs[i])
		gids, err := r.getGroupIDsByFileID(ctx, fd.ID)
		if err != nil {
			return nil, 0, err
		}
		fd.GroupIDs = gids
		results[i] = fd
	}
	return results, total, nil
}

// Delete deletes a FileRecord by ID.
func (r *GormFileRepository) Delete(ctx context.Context, id string) error {
	err := r.db.WithContext(ctx).Delete(&gormFileRecord{}, "id = ?", id).Error
	if err != nil {
		return err
	}
	return nil
}

// CountByHash counts total records referencing a hash globally (for deduplication count).
func (r *GormFileRepository) CountByHash(ctx context.Context, hash string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&gormFileRecord{}).Where("hash = ?", hash).Count(&count).Error
	if err != nil {
		return 0, err
	}
	return count, nil
}

// GetByFilenameOrHash checks conflict for a user (same filename or same content hash) and returns the record if found.
func (r *GormFileRepository) GetByFilenameOrHash(ctx context.Context, authorID string, filename string, hash string) (*file.FileRecord, error) {
	var record gormFileRecord
	err := r.db.WithContext(ctx).Model(&gormFileRecord{}).
		Preload("Tags").
		Where("author_id = ? AND (filename = ? OR hash = ?)", authorID, filename, hash).
		First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toDomainFile(&record), nil
}

// GetGroupPermission returns user's permission level in a file group.
func (r *GormFileRepository) GetGroupPermission(ctx context.Context, userID string, groupID string) (file.GroupPermission, bool, error) {
	var member gormFileGroupMember
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND file_group_id = ?", userID, groupID).
		First(&member).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return file.GroupPermission(member.Permission), true, nil
}

func (r *GormFileRepository) GetGroupIDsByFileID(ctx context.Context, fileID string) ([]string, error) {
	return r.getGroupIDsByFileID(ctx, fileID)
}

func (r *GormFileRepository) ReplaceFileGroups(ctx context.Context, fileID string, groupIDs []string) error {
	if err := r.db.WithContext(ctx).Where("file_id = ?", fileID).Delete(&gormFileGroupFile{}).Error; err != nil {
		return err
	}
	for _, gid := range groupIDs {
		if gid == "" {
			continue
		}
		row := gormFileGroupFile{
			FileID:      fileID,
			FileGroupID: gid,
		}
		if err := r.db.WithContext(ctx).
			Clauses(clause.OnConflict{DoNothing: true}).
			Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

// CreateWithOutbox inserts a FileRecord and an OutboxJob atomically in a transaction.
func (r *GormFileRepository) CreateWithOutbox(ctx context.Context, f *file.FileRecord, payload string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var gormTags []gormTag
		for _, tagName := range f.Tags {
			var gt gormTag
			seed := gormTag{
				ID:   xid.New().String(),
				Name: tagName,
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "name"}},
				DoNothing: true,
			}).Create(&seed).Error; err != nil {
				return err
			}
			if err := tx.Where("name = ?", tagName).First(&gt).Error; err != nil {
				return err
			}
			gormTags = append(gormTags, gt)
		}

		status := f.EmbeddingStatus
		if status == "" {
			status = "pending"
		}
		gf := &gormFileRecord{
			ID:              f.ID,
			Filename:        f.Filename,
			Description:     f.Description,
			Hash:            f.Hash,
			Size:            f.Size,
			ContentType:     f.ContentType,
			AuthorID:        f.AuthorID,
			Tags:            gormTags,
			EmbeddingStatus: status,
		}
		if err := tx.Create(gf).Error; err != nil {
			return err
		}

		// Replace file groups
		if err := tx.Where("file_id = ?", f.ID).Delete(&gormFileGroupFile{}).Error; err != nil {
			return err
		}
		for _, gid := range f.GroupIDs {
			if gid == "" {
				continue
			}
			row := gormFileGroupFile{
				FileID:      f.ID,
				FileGroupID: gid,
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
		}

		// Create outbox job
		oj := &gormOutboxJob{
			ID:        xid.New().String(),
			FileID:    f.ID,
			Payload:   payload,
			CreatedAt: time.Now(),
		}
		if err := tx.Create(oj).Error; err != nil {
			return err
		}

		f.CreatedAt = gf.CreatedAt
		f.UpdatedAt = gf.UpdatedAt
		f.EmbeddingStatus = gf.EmbeddingStatus
		return nil
	})
}

// GetPendingOutboxJobs retrieves a list of pending outbox jobs.
func (r *GormFileRepository) GetPendingOutboxJobs(ctx context.Context, limit int) ([]*file.OutboxJob, error) {
	var gormJobs []gormOutboxJob
	err := r.db.WithContext(ctx).Order("created_at ASC").Limit(limit).Find(&gormJobs).Error
	if err != nil {
		return nil, err
	}
	jobs := make([]*file.OutboxJob, len(gormJobs))
	for i, gj := range gormJobs {
		jobs[i] = &file.OutboxJob{
			ID:        gj.ID,
			FileID:    gj.FileID,
			Payload:   gj.Payload,
			CreatedAt: gj.CreatedAt,
		}
	}
	return jobs, nil
}

// DeleteOutboxJob deletes an outbox job by its ID.
func (r *GormFileRepository) DeleteOutboxJob(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Delete(&gormOutboxJob{}, "id = ?", id).Error
}

// DeleteOutboxJobByFileID deletes an outbox job by its associated FileID.
func (r *GormFileRepository) DeleteOutboxJobByFileID(ctx context.Context, fileID string) error {
	return r.db.WithContext(ctx).Delete(&gormOutboxJob{}, "file_id = ?", fileID).Error
}

// GetOrCreateHashRecord locks and increments/creates a file hash record, returning the new RefCount.
func (r *GormFileRepository) GetOrCreateHashRecord(ctx context.Context, hash string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var h gormFileHash
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("hash = ?", hash).First(&h).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			h = gormFileHash{
				Hash:     hash,
				RefCount: 1,
			}
			if err := tx.Create(&h).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			h.RefCount++
			if err := tx.Save(&h).Error; err != nil {
				return err
			}
		}
		count = int64(h.RefCount)
		return nil
	})
	return count, err
}

// DecrementHashRecord locks and decrements a file hash record, deleting it if count reaches 0, and returns the new RefCount.
func (r *GormFileRepository) DecrementHashRecord(ctx context.Context, hash string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var h gormFileHash
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("hash = ?", hash).First(&h).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			count = 0
			return nil
		} else if err != nil {
			return err
		}

		h.RefCount--
		if h.RefCount <= 0 {
			if err := tx.Delete(&h).Error; err != nil {
				return err
			}
			count = 0
		} else {
			if err := tx.Save(&h).Error; err != nil {
				return err
			}
			count = int64(h.RefCount)
		}
		return nil
	})
	return count, err
}

// CreateCleanupJob registers a new cleanup job.
func (r *GormFileRepository) CreateCleanupJob(ctx context.Context, job *file.CleanupJob) error {
	gj := &gormCleanupJob{
		ID:             job.ID,
		FileID:         job.FileID,
		Hash:           job.Hash,
		StorageKey:     job.StorageKey,
		DeletePhysical: job.DeletePhysical,
		Status:         job.Status,
		RetryCount:     job.RetryCount,
	}
	err := r.db.WithContext(ctx).Create(gj).Error
	if err != nil {
		return err
	}
	job.CreatedAt = gj.CreatedAt
	job.UpdatedAt = gj.UpdatedAt
	return nil
}

// GetPendingCleanupJobs fetches jobs with status pending or failed, ordered by created_at.
func (r *GormFileRepository) GetPendingCleanupJobs(ctx context.Context, limit int) ([]*file.CleanupJob, error) {
	var gormJobs []gormCleanupJob
	err := r.db.WithContext(ctx).
		Where("status = ? OR status = ?", "pending", "failed").
		Order("created_at ASC").
		Limit(limit).
		Find(&gormJobs).Error
	if err != nil {
		return nil, err
	}

	jobs := make([]*file.CleanupJob, len(gormJobs))
	for i, gj := range gormJobs {
		jobs[i] = &file.CleanupJob{
			ID:             gj.ID,
			FileID:         gj.FileID,
			Hash:           gj.Hash,
			StorageKey:     gj.StorageKey,
			DeletePhysical: gj.DeletePhysical,
			Status:         gj.Status,
			RetryCount:     gj.RetryCount,
			CreatedAt:      gj.CreatedAt,
			UpdatedAt:      gj.UpdatedAt,
		}
	}
	return jobs, nil
}

// UpdateCleanupJob updates status and retry count of a cleanup job.
func (r *GormFileRepository) UpdateCleanupJob(ctx context.Context, job *file.CleanupJob) error {
	return r.db.WithContext(ctx).
		Model(&gormCleanupJob{}).
		Where("id = ?", job.ID).
		Updates(map[string]interface{}{
			"status":      job.Status,
			"retry_count": job.RetryCount,
		}).Error
}

// DeleteCleanupJob deletes a cleanup job.
func (r *GormFileRepository) DeleteCleanupJob(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Delete(&gormCleanupJob{}, "id = ?", id).Error
}

// DeleteWithCleanup deletes a file record and creates a cleanup job atomically.
func (r *GormFileRepository) DeleteWithCleanup(ctx context.Context, id string, job *file.CleanupJob) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Delete file record
		if err := tx.Delete(&gormFileRecord{}, "id = ?", id).Error; err != nil {
			return err
		}
		// Create cleanup job
		gj := &gormCleanupJob{
			ID:             job.ID,
			FileID:         job.FileID,
			Hash:           job.Hash,
			StorageKey:     job.StorageKey,
			DeletePhysical: job.DeletePhysical,
			Status:         job.Status,
			RetryCount:     job.RetryCount,
		}
		if err := tx.Create(gj).Error; err != nil {
			return err
		}
		return nil
	})
}

