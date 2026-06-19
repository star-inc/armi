package usecase

import (
	"context"
	"sync"
	"testing"

	"github.com/star-inc/armi/internal/utils"
	"github.com/star-inc/armi/pkgs/user"
)

type mockUserRepo struct {
	mu           sync.Mutex
	usersByID    map[string]*user.User
	usersByName  map[string]*user.User
	createErr    error
	updateErr    error
	getIDErr     error
	getNameErr   error
	createdUsers []*user.User
	updatedUsers []*user.User
}

func (m *mockUserRepo) Create(ctx context.Context, u *user.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createErr != nil {
		return m.createErr
	}
	m.usersByID[u.ID] = u
	m.usersByName[u.Username] = u
	m.createdUsers = append(m.createdUsers, u)
	return nil
}

func (m *mockUserRepo) Update(ctx context.Context, u *user.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.updateErr != nil {
		return m.updateErr
	}
	m.usersByID[u.ID] = u
	m.usersByName[u.Username] = u
	m.updatedUsers = append(m.updatedUsers, u)
	return nil
}

func (m *mockUserRepo) GetByID(ctx context.Context, id string) (*user.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getIDErr != nil {
		return nil, m.getIDErr
	}
	return m.usersByID[id], nil
}

func (m *mockUserRepo) GetByUsername(ctx context.Context, username string) (*user.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getNameErr != nil {
		return nil, m.getNameErr
	}
	return m.usersByName[username], nil
}

type mockUserEventPublisher struct {
	mu        sync.Mutex
	events    []mockEvent
	publishErr error
}

type mockEvent struct {
	eventType string
	key       string
	payload   map[string]interface{}
}

func (m *mockUserEventPublisher) PublishEvent(ctx context.Context, eventType string, key string, payload map[string]interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.publishErr != nil {
		return m.publishErr
	}
	m.events = append(m.events, mockEvent{
		eventType: eventType,
		key:       key,
		payload:   payload,
	})
	return nil
}

func (m *mockUserEventPublisher) Close() error {
	return nil
}

func TestUserUsecase_Register(t *testing.T) {
	repo := &mockUserRepo{
		usersByID:   make(map[string]*user.User),
		usersByName: make(map[string]*user.User),
	}
	pub := &mockUserEventPublisher{}
	uc := NewUserUsecase(repo, pub)

	ctx := context.Background()

	// 1. Success path
	resp, err := uc.Register(ctx, "john_doe", "password123")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if resp.Username != "john_doe" {
		t.Errorf("expected username john_doe, got %s", resp.Username)
	}
	if len(repo.createdUsers) != 1 {
		t.Errorf("expected 1 user created in repo, got %d", len(repo.createdUsers))
	}
	if len(pub.events) != 1 || pub.events[0].eventType != "user.registered" {
		t.Errorf("expected user.registered event, got %+v", pub.events)
	}

	// 2. Already exists path
	_, err = uc.Register(ctx, "john_doe", "anotherpassword")
	if err == nil || err.Error() != "username already exists" {
		t.Errorf("expected 'username already exists' error, got %v", err)
	}
}

func TestUserUsecase_GetProfile(t *testing.T) {
	repo := &mockUserRepo{
		usersByID:   make(map[string]*user.User),
		usersByName: make(map[string]*user.User),
	}
	pub := &mockUserEventPublisher{}
	uc := NewUserUsecase(repo, pub)

	ctx := context.Background()

	// 1. User not found
	_, err := uc.GetProfile(ctx, "nonexistent")
	if err == nil || err.Error() != "user not found" {
		t.Errorf("expected 'user not found' error, got %v", err)
	}

	// 2. User found
	u := &user.User{ID: "user-1", Username: "alice"}
	repo.usersByID[u.ID] = u

	resp, err := uc.GetProfile(ctx, "user-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if resp.Username != "alice" || resp.ID != "user-1" {
		t.Errorf("unexpected profile response: %+v", resp)
	}
}

func TestUserUsecase_UpdateProfile(t *testing.T) {
	repo := &mockUserRepo{
		usersByID:   make(map[string]*user.User),
		usersByName: make(map[string]*user.User),
	}
	pub := &mockUserEventPublisher{}
	uc := NewUserUsecase(repo, pub)
	ctx := context.Background()

	u := &user.User{ID: "user-1", Username: "alice", PasswordHash: "old-hash"}
	repo.usersByID[u.ID] = u
	repo.usersByName[u.Username] = u

	// 1. User not found
	nonexistent := "nonexistent"
	_, err := uc.UpdateProfile(ctx, "nonexistent", &nonexistent, nil)
	if err == nil || err.Error() != "user not found" {
		t.Errorf("expected 'user not found' error, got %v", err)
	}

	// 2. Empty username error
	emptyUsername := ""
	_, err = uc.UpdateProfile(ctx, "user-1", &emptyUsername, nil)
	if err == nil || err.Error() != "username cannot be empty" {
		t.Errorf("expected 'username cannot be empty' error, got %v", err)
	}

	// 3. Username already exists
	bob := &user.User{ID: "user-2", Username: "bob"}
	repo.usersByID[bob.ID] = bob
	repo.usersByName[bob.Username] = bob

	newUsernameBob := "bob"
	_, err = uc.UpdateProfile(ctx, "user-1", &newUsernameBob, nil)
	if err == nil || err.Error() != "username already exists" {
		t.Errorf("expected 'username already exists' error, got %v", err)
	}

	// 4. Empty password error
	emptyPassword := ""
	_, err = uc.UpdateProfile(ctx, "user-1", nil, &emptyPassword)
	if err == nil || err.Error() != "password cannot be empty" {
		t.Errorf("expected 'password cannot be empty' error, got %v", err)
	}

	// 5. Successful Update of Username and Password
	newUsernameAliceUpdated := "alice_updated"
	newPassword := "new-password"
	resp, err := uc.UpdateProfile(ctx, "user-1", &newUsernameAliceUpdated, &newPassword)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if resp.Username != "alice_updated" {
		t.Errorf("expected username to be alice_updated, got %s", resp.Username)
	}

	if len(pub.events) != 1 || pub.events[0].eventType != "user.profile_updated" {
		t.Errorf("expected user.profile_updated event, got %+v", pub.events)
	}
	if pub.events[0].payload["username"] != "alice_updated" {
		t.Errorf("expected payload username 'alice_updated', got %v", pub.events[0].payload["username"])
	}
}

func TestUserUsecase_Authenticate(t *testing.T) {
	repo := &mockUserRepo{
		usersByID:   make(map[string]*user.User),
		usersByName: make(map[string]*user.User),
	}
	pub := &mockUserEventPublisher{}
	uc := NewUserUsecase(repo, pub)
	ctx := context.Background()

	// Hash a password first
	hash, err := utils.GenerateArgon2idHash("my-secret-pass")
	if err != nil {
		t.Fatal(err)
	}

	u := &user.User{ID: "user-1", Username: "charlie", PasswordHash: hash}
	repo.usersByID[u.ID] = u
	repo.usersByName[u.Username] = u

	// 1. User not found
	_, err = uc.Authenticate(ctx, "nonexistent", "my-secret-pass")
	if err == nil || err.Error() != "user not found" {
		t.Errorf("expected 'user not found' error, got %v", err)
	}

	// 2. Invalid password
	_, err = uc.Authenticate(ctx, "charlie", "wrong-pass")
	if err == nil || err.Error() != "invalid credentials" {
		t.Errorf("expected 'invalid credentials' error, got %v", err)
	}

	// 3. Successful authentication
	authU, err := uc.Authenticate(ctx, "charlie", "my-secret-pass")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if authU.ID != "user-1" {
		t.Errorf("expected user-1, got %s", authU.ID)
	}
}

func TestUserUsecase_GetByID(t *testing.T) {
	repo := &mockUserRepo{
		usersByID:   make(map[string]*user.User),
		usersByName: make(map[string]*user.User),
	}
	pub := &mockUserEventPublisher{}
	uc := NewUserUsecase(repo, pub)
	ctx := context.Background()

	// 1. User not found
	_, err := uc.GetByID(ctx, "nonexistent")
	if err == nil || err.Error() != "user not found" {
		t.Errorf("expected 'user not found' error, got %v", err)
	}

	// 2. Successful retrieval
	u := &user.User{ID: "user-1", Username: "dave"}
	repo.usersByID[u.ID] = u

	retU, err := uc.GetByID(ctx, "user-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if retU.Username != "dave" {
		t.Errorf("expected username 'dave', got %s", retU.Username)
	}
}
