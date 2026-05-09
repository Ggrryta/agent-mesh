// common-v2.js — 新模型(friendships / agents v2)用的共享库
// 与老 common.js 并存,老页面不受影响
// 所有新页面:<script src="common-v2.js"></script>

const API_BASE = window.location.origin

const getToken  = () => localStorage.getItem('ag_token')
const getAppID  = () => localStorage.getItem('ag_app_id')
const setAuth   = (appID, token) => { localStorage.setItem('ag_app_id', appID); localStorage.setItem('ag_token', token) }
const clearAuth = () => { localStorage.removeItem('ag_app_id'); localStorage.removeItem('ag_token') }

function setCurrentAgent(agentId) { localStorage.setItem('ag_current_agent', agentId) }
function getCurrentAgent() { return localStorage.getItem('ag_current_agent') }

async function apiFetch(path, opts = {}) {
  const token = getToken()
  const headers = {
    'Content-Type': 'application/json',
    ...(token ? { 'Authorization': 'Bearer ' + token } : {}),
    ...(opts.headers || {}),
  }
  const res = await fetch(API_BASE + path, { ...opts, headers })
  let body = {}
  try { body = await res.json() } catch (e) {}
  return { ok: res.ok, status: res.status, data: body }
}

// 专门给需要 X-Agent-ID 头部的接口(/friendships/* /v2/*)
async function agentFetch(path, agentId, opts = {}) {
  return apiFetch(path, {
    ...opts,
    headers: { ...(opts.headers || {}), 'X-Agent-ID': agentId },
  })
}

function requireLogin() {
  if (!getToken()) {
    location.href = 'login.html?next=' + encodeURIComponent(location.pathname + location.search)
    return false
  }
  return true
}

function renderNavV2(active) {
  const pages = [
    { href: 'index.html',      label: '🏠 首页' },
    { href: 'login.html',      label: '🔑 登录/注册' },
    { href: 'apikey-v2.html',  label: '🗝️ API Key' },
    { href: 'agents.html',     label: '🤖 我的 Agent' },
    { href: 'friends.html',    label: '👥 好友' },
    { href: 'monitor.html',    label: '📡 监控' },
    { href: 'directory.html',  label: '📇 目录' },
  ]
  // 兼容:如果当前就是 index-v2.html(旧链接),也高亮 index.html
  const activeNormalized = active === 'index-v2.html' ? 'index.html' : active
  const links = pages.map(p =>
    `<a href="${p.href}" ${p.href === activeNormalized ? 'class="active"' : ''}>${p.label}</a>`
  ).join('')
  const appID = getAppID()
  const user = appID
    ? `<span class="nav-user">👤 ${appID} <a href="#" onclick="logout()">退出</a></span>`
    : `<span class="nav-user"><a href="login.html">未登录</a></span>`
  return `<div class="nav">${links}${user}</div>`
}

function logout() {
  clearAuth()
  localStorage.removeItem('ag_current_agent')
  location.href = 'login.html'
}

function toast(msg, type = 'info') {
  const el = document.createElement('div')
  el.className = 'toast toast-' + type
  el.textContent = msg
  document.body.appendChild(el)
  setTimeout(() => el.remove(), 3000)
}

// 复制到剪贴板
function copy(text) {
  return navigator.clipboard.writeText(text)
    .then(() => toast('已复制到剪贴板', 'success'))
    .catch(() => toast('复制失败', 'error'))
}

function escapeHtml(s) {
  if (s == null) return ''
  return String(s)
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;').replace(/'/g, '&#39;')
}

// 从 API 响应中兼容提取列表:支持 data.items / data.list / 直接数组
function extractList(resp) {
  const d = resp && resp.data
  if (!d) return []
  if (Array.isArray(d)) return d
  if (Array.isArray(d.items)) return d.items
  if (Array.isArray(d.list)) return d.list
  return []
}

function formatTime(t) {
  if (!t) return ''
  const d = new Date(t)
  if (isNaN(d)) return t
  return d.toLocaleString('zh-CN', { hour12: false })
}

// 复用老的 COMMON_CSS
// 仅追加 v2 额外样式
const COMMON_CSS_V2 = `
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'Roboto', sans-serif; background: #f5f7fa; color: #2c3e50; line-height: 1.6; }
  .nav { background: white; padding: 12px 24px; display: flex; align-items: center; gap: 4px; box-shadow: 0 2px 4px rgba(0,0,0,0.08); flex-wrap: wrap; }
  .nav a { color: #1a73e8; text-decoration: none; padding: 6px 12px; border-radius: 4px; font-size: 14px; }
  .nav a:hover, .nav a.active { background: #e8f0fe; }
  .nav-user { margin-left: auto; font-size: 13px; color: #666; }
  .nav-user a { color: #e74c3c; font-size: 13px; padding: 2px 6px; }
  .container { max-width: 1100px; margin: 0 auto; padding: 24px; }
  h1 { color: #1a73e8; border-bottom: 3px solid #1a73e8; padding-bottom: 10px; margin-bottom: 20px; }
  h2 { color: #34495e; margin: 24px 0 12px; border-left: 4px solid #1a73e8; padding-left: 10px; }
  h3 { color: #555; margin: 16px 0 8px; }
  .card { background: white; border-radius: 6px; padding: 20px; box-shadow: 0 2px 4px rgba(0,0,0,0.1); margin-bottom: 20px; }
  .card-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 12px; }
  table { width: 100%; border-collapse: collapse; background: white; box-shadow: 0 2px 4px rgba(0,0,0,0.1); border-radius: 6px; overflow: hidden; }
  th, td { border: 1px solid #e0e0e0; padding: 10px 14px; text-align: left; font-size: 14px; }
  th { background: #1a73e8; color: white; font-weight: 600; }
  tr:nth-child(even) { background: #f8f9fa; }
  tr:hover { background: #e8f0fe; }
  .btn { display: inline-block; padding: 8px 16px; border-radius: 4px; border: none; cursor: pointer; font-size: 14px; font-weight: 500; transition: opacity .15s; text-decoration: none; }
  .btn:hover { opacity: .85; }
  .btn-primary { background: #1a73e8; color: white; }
  .btn-danger  { background: #e74c3c; color: white; }
  .btn-success { background: #27ae60; color: white; }
  .btn-outline { background: white; color: #1a73e8; border: 1px solid #1a73e8; }
  .btn-sm { padding: 4px 10px; font-size: 12px; }
  input, select, textarea { width: 100%; padding: 8px 12px; border: 1px solid #ddd; border-radius: 4px; font-size: 14px; font-family: inherit; }
  input:focus, select:focus, textarea:focus { outline: none; border-color: #1a73e8; box-shadow: 0 0 0 2px rgba(26,115,232,.15); }
  .form-row { margin-bottom: 14px; }
  .form-row label { display: block; font-size: 13px; font-weight: 500; margin-bottom: 4px; color: #555; }
  .form-row .hint { font-size: 12px; color: #888; margin-top: 4px; }
  .code { background: #2c3e50; color: #ecf0f1; padding: 14px; border-radius: 5px; font-family: 'Courier New', monospace; font-size: 13px; line-height: 1.5; overflow-x: auto; white-space: pre-wrap; word-break: break-all; }
  .badge { display: inline-block; padding: 2px 8px; border-radius: 10px; font-size: 12px; font-weight: 600; }
  .badge-green  { background: #d4edda; color: #155724; }
  .badge-red    { background: #f8d7da; color: #721c24; }
  .badge-yellow { background: #fff3cd; color: #856404; }
  .badge-blue   { background: #cce5ff; color: #004085; }
  .badge-gray   { background: #e2e3e5; color: #383d41; }
  .alert { padding: 12px 16px; border-radius: 4px; margin-bottom: 16px; font-size: 14px; }
  .alert-success { background: #d4edda; color: #155724; border-left: 4px solid #27ae60; }
  .alert-error   { background: #f8d7da; color: #721c24; border-left: 4px solid #e74c3c; }
  .alert-info    { background: #cce5ff; color: #004085; border-left: 4px solid #1a73e8; }
  .toast { position: fixed; bottom: 24px; right: 24px; padding: 12px 20px; border-radius: 6px; font-size: 14px; z-index: 9999; box-shadow: 0 4px 12px rgba(0,0,0,0.15); animation: fadeIn .2s; }
  .toast-info    { background: #1a73e8; color: white; }
  .toast-success { background: #27ae60; color: white; }
  .toast-error   { background: #e74c3c; color: white; }
  @keyframes fadeIn { from { opacity: 0; transform: translateY(10px); } to { opacity: 1; transform: translateY(0); } }
  .loading { color: #888; font-size: 14px; padding: 20px; text-align: center; }
  .empty { color: #aaa; font-size: 14px; padding: 30px; text-align: center; }
  .hero { text-align: center; padding: 40px 24px 24px; }
  .hero h1 { border: none; font-size: 2rem; margin-bottom: 8px; }
  .hero p { color: #666; font-size: 16px; }
  .cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(240px, 1fr)); gap: 16px; }
  .nav-card { background: white; border-radius: 8px; padding: 20px; box-shadow: 0 2px 8px rgba(0,0,0,0.08); text-decoration: none; color: inherit; display: block; transition: transform .15s, box-shadow .15s; border-top: 4px solid #1a73e8; }
  .nav-card:hover { transform: translateY(-3px); box-shadow: 0 6px 16px rgba(0,0,0,0.12); }
  .nav-card .icon { font-size: 1.8rem; margin-bottom: 10px; }
  .nav-card h3 { color: #1a73e8; margin-bottom: 4px; }
  .nav-card p { font-size: 13px; color: #666; }
  .stat-bar { display: flex; gap: 16px; flex-wrap: wrap; margin: 16px 0 24px; }
  .stat { background: white; padding: 12px 20px; border-radius: 6px; box-shadow: 0 2px 4px rgba(0,0,0,0.08); flex: 1; min-width: 140px; }
  .stat .num { font-size: 1.8rem; font-weight: 600; color: #1a73e8; }
  .stat .lbl { font-size: 12px; color: #888; }
  .row { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; }
  .mono { font-family: 'SF Mono', Monaco, 'Courier New', monospace; font-size: 13px; }
  .dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 6px; vertical-align: middle; }
  .dot-green { background: #27ae60; }
  .dot-gray  { background: #aaa; }
  .dot-red   { background: #e74c3c; }
  .step { background: #f8f9fa; border-left: 3px solid #1a73e8; padding: 12px 16px; margin-bottom: 10px; border-radius: 4px; }
  .step .step-title { font-weight: 600; color: #1a73e8; margin-bottom: 4px; font-size: 14px; }
  .step .step-desc { font-size: 13px; color: #666; }
`
