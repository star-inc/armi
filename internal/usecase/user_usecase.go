package usecase

import (
	"context"
	"errors"

	"github.com/rs/xid"
	"github.com/supersonictw/armi/internal/utils"
	"github.com/supersonictw/armi/pkgs/contract"
	"github.com/supersonictw/armi/pkgs/file"
	"github.com/supersonictw/armi/pkgs/user"
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
