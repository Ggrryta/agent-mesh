package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Ggrryta/agent-mesh/gateway/pkg/auth"
)

func newSigner(t *testing.T) *auth.Signer {
	t.Helper()
	s, err := auth.NewSigner("integration_secret_16b+", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRequireUser_OK(t *testing.T) {
	s := newSigner(t)
	tok, _ := s.IssueUser(99)

	hit := false
	h := RequireUser(s, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		uid, ok := UIDFromContext(r.Context())
		if !ok || uid != 99 {
			t.Fatalf("uid ctx: ok=%v v=%d", ok, uid)
		}
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(rr, req)
	if !hit {
		t.Fatal("handler not called")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
}

func TestRequireUser_MissingToken(t *testing.T) {
	s := newSigner(t)
	rr := httptest.NewRecorder()
	RequireUser(s, http.NotFoundHandler()).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}

func TestRequireUser_WrongKindRejected(t *testing.T) {
	s := newSigner(t)
	// Issue an agent token, then try to use it on a user-only route.
	tok, _ := s.IssueAgent("alice", 1, 0)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	RequireUser(s, http.NotFoundHandler()).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rr.Code)
	}
}

func TestRequireAgent_OK(t *testing.T) {
	s := newSigner(t)
	tok, _ := s.IssueAgent("alice", 7, 0)
	hit := false
	h := RequireAgent(s, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		c := ClaimsFromContext(r.Context())
		if c == nil || c.AgentID != "alice" || c.UID != 7 {
			t.Fatalf("claims: %+v", c)
		}
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(rr, req)
	if !hit {
		t.Fatal("handler not called")
	}
}

func TestRequireUser_MalformedHeader(t *testing.T) {
	s := newSigner(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Basic abcdef")
	RequireUser(s, http.NotFoundHandler()).ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}
