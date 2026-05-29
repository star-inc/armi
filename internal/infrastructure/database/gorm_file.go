package database

import (
	"context"
	"errors"

	"github.com/rs/xid"
	"github.com/supersonictw/armi/pkgs/file"
	"gorm.io/gorm"
)

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
		ID:          g.ID,
		Filename:    g.Filename,
		Hash:        g.Hash,
		Size:        g.Size,
		ContentType: g.ContentType,
		OwnerID:     g.OwnerID,
		Tags:        tags,
		CreatedAt:   g.CreatedAt,
		UpdatedAt:   g.UpdatedAt,
	}
}

// Create inserts a FileRecord entity into database.
func (r *GormFileRepository) Create(ctx context.Context, f *file.FileRecord) error {
	var gormTags []gormTag
	for _, tagName := range f.Tags {
		var gt gormTag
		err := r.db.WithContext(ctx).Where("name = ?", tagName).FirstOrCreate(&gt, gormTag{
			ID:   xid.New().String(),
			Name: tagName,
		}).Error
		if err != nil {
			return err
		}
		gormTags = append(gormTags, gt)
	}

	gf := &gormFileRecord{
		ID:          f.ID,
		Filename:    f.Filename,
		Hash:        f.Hash,
		Size:        f.Size,
		ContentType: f.ContentType,
		OwnerID:     f.OwnerID,
		Tags:        gormTags,
	}
	err := r.db.WithContext(ctx).Create(gf).Error
	if err != nil {
		return err
	}
	f.CreatedAt = gf.CreatedAt
	f.UpdatedAt = gf.UpdatedAt
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
	return toDomainFile(&gf), nil
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
	return toDomainFile(&gf), nil
}

// ListByOwnerID fetches all FileRecords owned by a user, optionally filtered by tag.
func (r *GormFileRepository) ListByOwnerID(ctx context.Context, ownerID string, tag string) ([]*file.FileRecord, error) {
	var gfs []gormFileRecord
	query := r.db.WithContext(ctx).Preload("Tags").Where("owner_id = ?", ownerID)
	if tag != "" {
		query = query.Where("id IN (SELECT gorm_file_record_id FROM file_tags JOIN tags ON tags.id = file_tags.gorm_tag_id WHERE tags.name = ?)", tag)
	}
	err := query.Find(&gfs).Error
	if err != nil {
		return nil, err
	}

	results := make([]*file.FileRecord, len(gfs))
	for i := range gfs {
		results[i] = toDomainFile(&gfs[i])
	}
	return results, nil
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

// CountByFilenameOrHash checks conflict for a user (same filename or same content hash).
func (r *GormFileRepository) CountByFilenameOrHash(ctx context.Context, ownerID string, filename string, hash string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&gormFileRecord{}).
		Where("owner_id = ? AND (filename = ? OR hash = ?)", ownerID, filename, hash).
		Count(&count).Error
	if err != nil {
		return 0, err
	}
	return count, nil
}
