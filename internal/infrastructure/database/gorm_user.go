package database

import (
	"context"
	"errors"

	"github.com/star-inc/armi/pkgs/user"
	"gorm.io/gorm"
)

// GormUserRepository implements user.UserRepository interface.
type GormUserRepository struct {
	db *gorm.DB
}

// NewGormUserRepository constructs a new GormUserRepository.
func NewGormUserRepository(db *gorm.DB) user.UserRepository {
	return &GormUserRepository{db: db}
}

// toDomainUser maps internal database schema gormUser to domain User entity.
func toDomainUser(g *gormUser) *user.User {
	if g == nil {
		return nil
	}
	return &user.User{
		ID:           g.ID,
		Username:     g.Username,
		PasswordHash: g.PasswordHash,
		CreatedAt:    g.CreatedAt,
		UpdatedAt:    g.UpdatedAt,
	}
}

// Create inserts a user entity into database.
func (r *GormUserRepository) Create(ctx context.Context, u *user.User) error {
	gu := &gormUser{
		ID:           u.ID,
		Username:     u.Username,
		PasswordHash: u.PasswordHash,
	}
	err := r.db.WithContext(ctx).Create(gu).Error
	if err != nil {
		return err
	}
	u.CreatedAt = gu.CreatedAt
	u.UpdatedAt = gu.UpdatedAt
	return nil
}

// Update modifies username/password hash of an existing user.
func (r *GormUserRepository) Update(ctx context.Context, u *user.User) error {
	updates := map[string]interface{}{
		"username":      u.Username,
		"password_hash": u.PasswordHash,
	}
	if err := r.db.WithContext(ctx).Model(&gormUser{}).Where("id = ?", u.ID).Updates(updates).Error; err != nil {
		return err
	}
	// refresh updated fields
	latest, err := r.GetByID(ctx, u.ID)
	if err != nil {
		return err
	}
	if latest != nil {
		u.UpdatedAt = latest.UpdatedAt
		u.CreatedAt = latest.CreatedAt
	}
	return nil
}

// GetByID finds a user by ID. Returns nil, nil if not found.
func (r *GormUserRepository) GetByID(ctx context.Context, id string) (*user.User, error) {
	var gu gormUser
	err := r.db.WithContext(ctx).First(&gu, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toDomainUser(&gu), nil
}

// GetByUsername finds a user by Username. Returns nil, nil if not found.
func (r *GormUserRepository) GetByUsername(ctx context.Context, username string) (*user.User, error) {
	var gu gormUser
	err := r.db.WithContext(ctx).First(&gu, "username = ?", username).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toDomainUser(&gu), nil
}
