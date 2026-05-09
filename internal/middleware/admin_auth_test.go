package middleware

import (
	"context"
	"testing"

	"agent-gateway/pkg/ctxkey"

	"github.com/cloudwego/hertz/pkg/app"
	hconfig "github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/route"
)

func newTestEngine() *route.Engine {
	return route.NewEngine(hconfig.NewOptions(nil))
}

func TestAdminAuth_RejectsWhenTokenNotConfigured(t *testing.T) {
	engine := newTestEngine()
	engine.GET("/admin", AdminAuth(""), func(ctx context.Context, c *app.RequestContext) {
		c.String(consts.StatusOK, "ok")
	})

	resp := ut.PerformRequest(engine, consts.MethodGet, "/admin", nil).Result()
	if resp.StatusCode() != consts.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode())
	}
}

func TestAdminAuth_RequiresMatchingToken(t *testing.T) {
	engine := newTestEngine()
	engine.GET("/admin", AdminAuth("secret-token"), func(ctx context.Context, c *app.RequestContext) {
		isAdmin, _ := c.Get(ctxkey.Admin)
		admin, _ := isAdmin.(bool)
		if !admin {
			t.Fatalf("expected admin flag to be set")
		}
		c.String(consts.StatusOK, "ok")
	})

	unauthorized := ut.PerformRequest(engine, consts.MethodGet, "/admin", nil).Result()
	if unauthorized.StatusCode() != consts.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", unauthorized.StatusCode())
	}

	authorized := ut.PerformRequest(
		engine,
		consts.MethodGet,
		"/admin",
		nil,
		ut.Header{Key: "X-Admin-Token", Value: "secret-token"},
	).Result()
	if authorized.StatusCode() != consts.StatusOK {
		t.Fatalf("expected 200, got %d", authorized.StatusCode())
	}
}

func TestRequireAdminToken(t *testing.T) {
	if RequireAdminToken("   ") {
		t.Fatalf("expected blank token to be rejected")
	}
	if !RequireAdminToken("secret-token") {
		t.Fatalf("expected non-empty token to be accepted")
	}
}
