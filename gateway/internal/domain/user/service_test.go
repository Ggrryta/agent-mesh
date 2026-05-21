package user

import (
	"context"
	"errors"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// memRepo 是测试用的非协程安全内存实现。
type memRepo struct {
	byID       map[int64]*User
	byUsername map[string]*User
	nextID     int64
}

func newMemRepo() *memRepo {
	return &memRepo{
		byID:       map[int64]*User{},
		byUsername: map[string]*User{},
		nextID:     0,
	}
}

func (r *memRepo) CreateWithVirtualAgent(_ context.Context, username, hash string) (*User, error) {
	if _, ok := r.byUsername[username]; ok {
		return nil, ErrUsernameTaken
	}
	r.nextID++
	u := &User{
		ID:                 r.nextID,
		Username:           username,
		PasswordHash:       hash,
		VirtualUserAgentID: VirtualAgentIDFor(r.nextID),
	}
	r.byID[u.ID] = u
	r.byUsername[u.Username] = u
	return u, nil
}

func (r *memRepo) GetByUsername(_ context.Context, username string) (*User, error) {
	u, ok := r.byUsername[username]
	if !ok {
		return nil, ErrUserNotFound
	}
	return u, nil
}

func (r *memRepo) GetByID(_ context.Context, id int64) (*User, error) {
	u, ok := r.byID[id]
	if !ok {
		return nil, ErrUserNotFound
	}
	return u, nil
}

func TestRegister_Success(t *testing.T) {
	svc := NewService(newMemRepo())
	u, err := svc.Register(context.Background(), "alice", "secretpw")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if u.Username != "alice" {
		t.Fatalf("username: %q", u.Username)
	}
	if u.VirtualUserAgentID != "virtual-user-1" {
		t.Fatalf("virtual id: %q", u.VirtualUserAgentID)
	}
	if u.PasswordHash == "secretpw" {
		t.Fatal("password must be hashed, not plain")
	}
	// Verify bcrypt round-trip.
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte("secretpw")); err != nil {
		t.Fatalf("bcrypt compare: %v", err)
	}
}

func TestRegister_Normalization(t *testing.T) {
	svc := NewService(newMemRepo())
	if _, err := svc.Register(context.Background(), "  Alice  ", "secretpw"); err != nil {
		t.Fatalf("register: %v", err)
	}
	// Duplicate check under normalization.
	if _, err := svc.Register(context.Background(), "ALICE", "secretpw"); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("want ErrUsernameTaken, got %v", err)
	}
}

func TestRegister_BadUsername(t *testing.T) {
	svc := NewService(newMemRepo())
	for _, u := range []string{"", "ab", "has space", "a!b", "too_long_" + string(make([]byte, 80))} {
		if _, err := svc.Register(context.Background(), u, "secretpw"); err == nil {
			t.Errorf("want error for %q", u)
		}
	}
}

func TestRegister_ShortPassword(t *testing.T) {
	svc := NewService(newMemRepo())
	if _, err := svc.Register(context.Background(), "alice", "12345"); err == nil {
		t.Fatal("want password-length error")
	}
}

func TestLogin_Success(t *testing.T) {
	svc := NewService(newMemRepo())
	_, _ = svc.Register(context.Background(), "alice", "secretpw")
	u, err := svc.Login(context.Background(), "alice", "secretpw")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if u.Username != "alice" {
		t.Fatalf("user %+v", u)
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	svc := NewService(newMemRepo())
	_, _ = svc.Register(context.Background(), "alice", "secretpw")
	if _, err := svc.Login(context.Background(), "alice", "wrong"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("want ErrInvalidPassword, got %v", err)
	}
}

func TestLogin_UnknownUserLeaksNothing(t *testing.T) {
	svc := NewService(newMemRepo())
	// Unknown username must surface the same error as wrong password.
	if _, err := svc.Login(context.Background(), "nobody", "whatever"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("want ErrInvalidPassword, got %v", err)
	}
}
