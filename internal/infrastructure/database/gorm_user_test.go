package database

import (
	"context"
	"testing"

	"github.com/star-inc/armi/pkgs/user"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestGormUserRepository(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}

	if err := db.AutoMigrate(&gormUser{}); err != nil {
		t.Fatal(err)
	}

	repo := NewGormUserRepository(db)
	ctx := context.Background()

	// 1. Test Create
	u := &user.User{
		ID:           "user-1",
		Username:     "alice",
		PasswordHash: "hash-123",
	}

	err = repo.Create(ctx, u)
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	if u.CreatedAt.IsZero() || u.UpdatedAt.IsZero() {
		t.Errorf("expected timestamps to be set, got CreatedAt=%v UpdatedAt=%v", u.CreatedAt, u.UpdatedAt)
	}

	// 2. Test GetByID
	dbUser, err := repo.GetByID(ctx, "user-1")
	if err != nil {
		t.Fatalf("failed to get user by ID: %v", err)
	}
	if dbUser == nil {
		t.Fatal("expected user to be found")
	}
	if dbUser.Username != "alice" || dbUser.PasswordHash != "hash-123" {
		t.Errorf("incorrect user content: %+v", dbUser)
	}

	// Test GetByID - Not Found
	dbUserNotFound, err := repo.GetByID(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("expected no error for nonexistent GetByID, got %v", err)
	}
	if dbUserNotFound != nil {
		t.Errorf("expected nil user for nonexistent ID, got %+v", dbUserNotFound)
	}

	// 3. Test GetByUsername
	dbUserByName, err := repo.GetByUsername(ctx, "alice")
	if err != nil {
		t.Fatalf("failed to get user by username: %v", err)
	}
	if dbUserByName == nil {
		t.Fatal("expected user to be found by username")
	}
	if dbUserByName.ID != "user-1" {
		t.Errorf("incorrect user ID: %s", dbUserByName.ID)
	}

	// Test GetByUsername - Not Found
	dbUserByNameNotFound, err := repo.GetByUsername(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("expected no error for nonexistent GetByUsername, got %v", err)
	}
	if dbUserByNameNotFound != nil {
		t.Errorf("expected nil user for nonexistent username, got %+v", dbUserByNameNotFound)
	}

	// 4. Test Update
	u.Username = "alice_new"
	u.PasswordHash = "hash-456"
	err = repo.Update(ctx, u)
	if err != nil {
		t.Fatalf("failed to update user: %v", err)
	}

	// Verify update in DB
	updatedUser, err := repo.GetByID(ctx, "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if updatedUser.Username != "alice_new" || updatedUser.PasswordHash != "hash-456" {
		t.Errorf("expected updated username 'alice_new' and password hash 'hash-456', got %+v", updatedUser)
	}
}
