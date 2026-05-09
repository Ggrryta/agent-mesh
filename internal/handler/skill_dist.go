package handler

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"agent-gateway/pkg/resp"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// SkillDistHandler 对外暴露 skill 分发接口:版本查询 + 包下载
// 镜像 build 时将 skill tarball 固化到 /app/skill-dist/ 下,本 handler 透出
//
// 设计取舍:
//  1. 未鉴权 — skill 在升级前可能还不知道 API Key,且版本号属于非敏感信息
//  2. 下载端点也未鉴权 — tarball 同样不敏感,任何接入者都应能拉到自己即将运行的代码
//  3. sha256 与 version 同源返回,防止 /skill/download 被篡改后校验仍通过
type SkillDistHandler struct {
	distDir string // 默认 /app/skill-dist
}

func NewSkillDistHandler(distDir string) *SkillDistHandler {
	if distDir == "" {
		distDir = "/app/skill-dist"
	}
	return &SkillDistHandler{distDir: distDir}
}

// Version GET /skill/version
// 返回 {version, sha256}。缺文件时返回 404 让 skill 知道该 gateway 没携带 tarball。
func (h *SkillDistHandler) Version(ctx context.Context, c *app.RequestContext) {
	versionPath := filepath.Join(h.distDir, "skill-dist.version")
	shaPath := filepath.Join(h.distDir, "skill-dist.sha256")

	versionBytes, err := os.ReadFile(versionPath)
	if err != nil {
		c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "skill tarball not bundled with this gateway"))
		return
	}
	shaBytes, err := os.ReadFile(shaPath)
	if err != nil {
		c.JSON(consts.StatusInternalServerError, resp.Err(resp.CodeInternalServerError, "sha256 missing"))
		return
	}
	c.JSON(consts.StatusOK, resp.OK(map[string]any{
		"version": strings.TrimSpace(string(versionBytes)),
		"sha256":  strings.TrimSpace(string(shaBytes)),
	}))
}

// Download GET /skill/download
// 直接流式返回 tarball。self_update.py 下载后应根据 Version 接口返回的 sha256 校验。
func (h *SkillDistHandler) Download(ctx context.Context, c *app.RequestContext) {
	tarballPath := filepath.Join(h.distDir, "skill-dist.tar.gz")
	info, err := os.Stat(tarballPath)
	if err != nil {
		c.JSON(consts.StatusNotFound, resp.Err(resp.CodeNotFound, "skill tarball not bundled with this gateway"))
		return
	}
	c.Response.Header.Set("Content-Type", "application/gzip")
	c.Response.Header.Set("Content-Disposition", `attachment; filename="agent-gateway-skill.tar.gz"`)
	c.Response.Header.SetContentLength(int(info.Size()))
	c.File(tarballPath)
}
