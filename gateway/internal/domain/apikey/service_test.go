package apikey

import (
	"context"
	"sync"
	"testing"
	"time"
)

// memRepo 是 service 测试用的内存实现，goroutine-safe。
type memRepo struct {
	mu     sync.Mutex
	byHash map[string]*Key
	byID   map[int64]*Key
	nextID int64
}

func newMemRepo() *memRepo {
	return &memRepo{byHash: map[string]*Key{}, byID: map[int64]*Key{}}
}

func (r *memRepo) Insert(ctx context.Context, ownerUID int64, keyHash, prefix, label string) (*Key, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	k := &Key{
		ID:        r.nextID,
		OwnerUID:  ownerUID,
		KeyPrefix: prefix,
		Label:     label,
		CreatedAt: time.Now(),
	}
	r.byHash[keyHash] = k
	r.byID[k.ID] = k
	return cloneKey(k), nil
}

func (r *memRepo) FindByHash(ctx context.Context, keyHash string) (*Key, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.byHash[keyHash]
	if !ok {
		return nil, ErrKeyNotFound
	}
	return cloneKey(k), nil
}

func (r *memRepo) ListByOwner(ctx context.Context, ownerUID int64) ([]*Key, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Key, 0)
	for _, k := range r.byID {
		if k.OwnerUID == ownerUID {
			out = append(out, cloneKey(k))
		}
	}
	return out, nil
}

func (r *memRepo) Revoke(ctx context.Context, ownerUID, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.byID[id]
	if !ok || k.OwnerUID != ownerUID {
		return ErrKeyNotFound
	}
	if k.RevokedAt != nil {
		return nil // 幂等
	}
	now := time.Now()
	k.RevokedAt = &now
	return nil
}

func (r *memRepo) TouchLastUsed(ctx context.Context, id int64, ts time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if k, ok := r.byID[id]; ok {
		t := ts
		k.LastUsedAt = &t
	}
	return nil
}

func cloneKey(k *Key) *Key {
	cp := *k
	if k.LastUsedAt != nil {
		t := *k.LastUsedAt
		cp.LastUsedAt = &t
	}
	if k.RevokedAt != nil {
		t := *k.RevokedAt
		cp.RevokedAt = &t
	}
	return &cp
}

// ── 测试 ──

func TestService_IssueAndVerify(t *testing.T) {
	svc := NewService(newMemRepo(), nil)

	raw, k, err := svc.Issue(context.Background(), 42, "ci")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if k.OwnerUID != 42 || k.Label != "ci" || k.KeyPrefix != extractPrefix(raw) {
		t.Fatalf("issued key mismatched: %+v", k)
	}
	if len(raw) < 40 {
		t.Fatalf("raw too short: %d", len(raw))
	}

	got, err := svc.Verify(context.Background(), raw)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.ID != k.ID {
		t.Fatalf("verify returned different key: %d vs %d", got.ID, k.ID)
	}
}

func TestService_VerifyRejectsUnknownKey(t *testing.T) {
	svc := NewService(newMemRepo(), nil)
	_, err := svc.Verify(context.Background(), "sk-am_"+mkPad(40))
	if err == nil {
		t.Fatal("want error for unknown key")
	}
}

func TestService_VerifyRejectsBadFormat(t *testing.T) {
	svc := NewService(newMemRepo(), nil)
	_, err := svc.Verify(context.Background(), "not-our-prefix")
	if err != ErrKeyInvalid {
		t.Fatalf("want ErrKeyInvalid, got %v", err)
	}
}

func TestService_VerifyRejectsRevokedKey(t *testing.T) {
	svc := NewService(newMemRepo(), nil)
	raw, k, _ := svc.Issue(context.Background(), 1, "")

	if err := svc.Revoke(context.Background(), 1, k.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	_, err := svc.Verify(context.Background(), raw)
	if err != ErrKeyRevoked {
		t.Fatalf("want ErrKeyRevoked, got %v", err)
	}
}

func TestService_RevokeIsIdempotent(t *testing.T) {
	svc := NewService(newMemRepo(), nil)
	_, k, _ := svc.Issue(context.Background(), 1, "")
	if err := svc.Revoke(context.Background(), 1, k.ID); err != nil {
		t.Fatalf("first revoke: %v", err)
	}
	if err := svc.Revoke(context.Background(), 1, k.ID); err != nil {
		t.Fatalf("second revoke should be no-op, got %v", err)
	}
}

func TestService_RevokeDifferentOwnerIsNotFound(t *testing.T) {
	svc := NewService(newMemRepo(), nil)
	_, k, _ := svc.Issue(context.Background(), 1, "")
	err := svc.Revoke(context.Background(), 999, k.ID)
	if err != ErrKeyNotFound {
		t.Fatalf("want ErrKeyNotFound, got %v", err)
	}
}

func TestService_List(t *testing.T) {
	svc := NewService(newMemRepo(), nil)
	_, _, _ = svc.Issue(context.Background(), 1, "a")
	_, _, _ = svc.Issue(context.Background(), 1, "b")
	_, _, _ = svc.Issue(context.Background(), 2, "other")

	got, err := svc.List(context.Background(), 1)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
}

func mkPad(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}
