package resp

// 错误码定义
const (
	CodeOK                  = 0
	CodeBadRequest          = 400
	CodeUnauthorized        = 401
	CodeForbidden           = 403
	CodeNotFound            = 404
	CodeTooManyRequests     = 429
	CodeInternalServerError = 500
	CodeBadGateway          = 502
	CodeServiceUnavailable  = 503

	// GAS 业务错误码(9xxx 段)
	CodeAgentOffline     = 9001 // 目标 agent 离线,无法投递消息
	CodeNotFriend        = 9002 // 双方非好友,不能通信
	CodeFriendPending    = 9003 // 加好友请求待处理
	CodeTaskClosed       = 9004 // task 已关闭
	CodeTaskNotMember    = 9005 // 调用方不是 task 成员
	CodeAgentConflict    = 9006 // 同一 agent_id 已在别处在线
	CodeRateLimited      = 9007 // 触发速率限制
)

// Response 统一响应结构
type Response struct {
	Code      int    `json:"code"`
	Msg       string `json:"msg"`
	Data      any    `json:"data"`
	RequestID string `json:"request_id,omitempty"` // 请求 ID，方便追踪
}

// OK 成功响应
func OK(data any) Response {
	return Response{Code: CodeOK, Msg: "ok", Data: data}
}

// Err 错误响应
func Err(code int, msg string) Response {
	return Response{Code: code, Msg: msg, Data: nil}
}

// ErrWithData 带结构化数据的错误响应
func ErrWithData(code int, msg string, data any) Response {
	return Response{Code: code, Msg: msg, Data: data}
}

// WithRequestID 为响应添加 Request ID
func (r Response) WithRequestID(requestID string) Response {
	r.RequestID = requestID
	return r
}
