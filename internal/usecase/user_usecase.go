package usecase

import (
	"context"
	"errors"
	"strings"

	"github.com/rs/xid"
	"github.com/star-inc/armi/internal/utils"
	"github.com/star-inc/armi/pkgs/contract"
	"github.com/star-inc/armi/pkgs/file"
	"github.com/star-inc/armi/pkgs/user"
)

// UserUsecase coordinates user related use cases.
type UserUsecase struct {
	userRepo  user.UserRepository
	publisher file.EventPublisher
}

// NewUserUsecase constructs a new UserUsecase.
func NewUserUsecase(userRepo user.UserRepository, publisher file.EventPublisher) *UserUsecase {
	return &UserUsecase{
		userRepo:  userRepo,
		publisher: publisher,
	}
}

// Register registers a new user, hashes password, and publishes user.registered event.
func (uc *UserUsecase) Register(ctx context.Context, username, password string) (*contract.RegisterResponse, error) {
	existing, err := uc.userRepo.GetByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, errors.New("username already exists")
	}

	hash, err := utils.GenerateArgon2idHash(password)
	if err != nil {
		return nil, err
	}

	newUser := &user.User{
		ID:           xid.New().String(),
		Username:     username,
		PasswordHash: hash,
	}

	err = uc.userRepo.Create(ctx, newUser)
	if err != nil {
		return nil, err
	}

	uc.publisher.PublishEvent(ctx, "user.registered", newUser.ID, map[string]interface{}{
		"username": username,
	})

	return &contract.RegisterResponse{
		ID:        newUser.ID,
		Username:  newUser.Username,
		CreatedAt: newUser.CreatedAt,
	}, nil
}

// GetProfile returns the current user profile.
func (uc *UserUsecase) GetProfile(ctx context.Context, userID string) (*contract.MeResponse, error) {
	u, err := uc.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, errors.New("user not found")
	}
	return &contract.MeResponse{
		ID:        u.ID,
		Username:  u.Username,
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}, nil
}

// UpdateProfile updates current user's username and/or password.
func (uc *UserUsecase) UpdateProfile(ctx context.Context, userID string, username, password *string) (*contract.MeResponse, error) {
	u, err := uc.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, errors.New("user not found")
	}

	if username != nil {
		newUsername := strings.TrimSpace(*username)
		if newUsername == "" {
			return nil, errors.New("username cannot be empty")
		}
		if newUsername != u.Username {
			existing, err := uc.userRepo.GetByUsername(ctx, newUsername)
			if err != nil {
				return nil, err
			}
			if existing != nil {
				return nil, errors.New("username already exists")
			}
		}
		u.Username = newUsername
	}

	if password != nil {
		if *password == "" {
			return nil, errors.New("password cannot be empty")
		}
		hash, err := utils.GenerateArgon2idHash(*password)
		if err != nil {
			return nil, err
		}
		u.PasswordHash = hash
	}

	if err := uc.userRepo.Update(ctx, u); err != nil {
		return nil, err
	}

	uc.publisher.PublishEvent(ctx, "user.profile_updated", u.ID, map[string]interface{}{
		"username": u.Username,
	})

	return &contract.MeResponse{
		ID:        u.ID,
		Username:  u.Username,
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}, nil
}

// Authenticate verifies password against username and returns the User domain entity.
func (uc *UserUsecase) Authenticate(ctx context.Context, username, password string) (*user.User, error) {
	u, err := uc.userRepo.GetByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, errors.New("user not found")
	}

	match, err := utils.VerifyArgon2id(password, u.PasswordHash)
	if err != nil {
		return nil, err
	}
	if !match {
		return nil, errors.New("invalid credentials")
	}

	return u, nil
}

// GetByID looks up a user by their ID (used for JWT sub claim resolution).
func (uc *UserUsecase) GetByID(ctx context.Context, id string) (*user.User, error) {
	u, err := uc.userRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, errors.New("user not found")
	}
	return u, nil
}
