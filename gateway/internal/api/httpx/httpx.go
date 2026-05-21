// Package httpx 放所有 api/* 路由共享的 HTTP helper：响应外形、JSON 解码、
// 错误映射等。
//
// 单独出来让 admin 和 mesh 层用同一套响应格式，免得到处复制。
package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// ErrorBody 是 gateway 所有错误响应的统一形状。
// 对应 docs/api.md §Error Responses。
type ErrorBody struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// WriteJSON 用指定 status 写 application/json。
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError 写一个标准化的错误响应。
func WriteError(w http.ResponseWriter, httpStatus, appCode int, msg string) {
	WriteJSON(w, httpStatus, ErrorBody{Code: appCode, Message: msg})
}

// DecodeJSON 读取请求 body 并反序列化到 v。多数 handler 把缺 body 视为错误，
// 需要特殊处理 io.EOF 的调用方自己判断。
//
// body 上限 1 MiB，防止攻击者推巨型 JSON。
const maxBody = 1 << 20

// ErrBadRequest 是解码错误的哨兵值，handler 据此返 HTTP 400。
var ErrBadRequest = errors.New("httpx: bad request")

// 应用错误码（详见 docs/api.md）。
// 4 位 HTTP 前缀 + 2 位子类：让 SDK 按子类决定重试 / 通知用户的行为。
const (
	// 通用
	CodeBadRequest = 40001 // 请求体 / 参数不合法
	CodeLoginFail  = 40100 // 用户名或密码错
	CodeForbidden  = 40300 // 权限不足
	CodeNotOwner   = 40301 // 不是资源 owner
	CodeNotFound   = 40400 // 资源不存在
	CodeConflict   = 40900 // 唯一键冲突
	CodeInternal   = 50000 // 服务端未知错误
	CodeNotImpl    = 50001 // 功能未上线

	// 认证子类（供 SDK 区分重试策略）
	// 40110：JWT 过期 → SDK 自动刷新重试一次
	// 40111：JWT 签名 / 格式错 → 不重试
	// 40112：API Key 被吊销 → 通知用户，不重试
	// 40113：`/auth/token` 请求里 agent_id 不属于该 key 的 owner → 不重试
	CodeTokenExpired  = 40110
	CodeTokenInvalid  = 40111
	CodeKeyRevoked    = 40112
	CodeAgentNotOwned = 40113
)

// DecodeJSON 从请求 body 解码到 v。body 为空 / 格式错 / 超大时返回 ErrBadRequest。
func DecodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return ErrBadRequest
	}
	r.Body = http.MaxBytesReader(nil, r.Body, maxBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return ErrBadRequest
		}
		return ErrBadRequest
	}
	return nil
}
