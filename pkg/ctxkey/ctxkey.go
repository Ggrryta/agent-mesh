package ctxkey

// ContextKeyAppID 鉴权中间件注入 app_id 的 context key
const AppID = "auth_app_id"

// Admin 管理鉴权中间件注入的管理员标记。
const Admin = "auth_admin"

// AgentID 由 AgentAuth 中间件注入,表示当前请求代表哪个 agent 身份
// (当 app_id 名下有多个 agent 时,通过 X-Agent-ID header 或 :agent_id 路径参数指定)
const AgentID = "auth_agent_id"
