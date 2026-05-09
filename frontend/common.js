const API_BASE = window.location.origin

const getToken  = () => localStorage.getItem('ag_token')
const getAppID  = () => localStorage.getItem('ag_app_id')
const setAuth   = (appID, token) => { localStorage.setItem('ag_app_id', appID); localStorage.setItem('ag_token', token) }
const clearAuth = () => { localStorage.removeItem('ag_app_id'); localStorage.removeItem('ag_token') }

async function apiFetch(path, opts = {}) {
  const token = getToken()
  const headers = { 'Content-Type': 'application/json', ...(token ? { 'Authorization': 'Bearer ' + token } : {}), ...(opts.headers || {}) }
  const res = await fetch(API_BASE + path, { ...opts, headers })
  const body = await res.json().catch(() => ({}))
  return { ok: res.ok, status: res.status, data: body }
}

// 导航栏 HTML（当前页高亮）
function renderNav(active) {
  const pages = [
    { href: 'index.html',         label: '🏠 首页' },
    { href: 'auth.html',          label: '🔑 注册/登录' },
    { href: 'skills.html',        label: '⚡ Skill 管理' },
    { href: 'apply.html',         label: '📋 权限申请' },
    { href: 'invoke.html',        label: '🚀 调用测试台' },
    { href: 'notifications.html', label: '🔔 通知' },
    { href: 'apikey.html',        label: '🗝️ API Key' },
  ]
  const links = pages.map(p =>
    `<a href="${p.href}" ${p.href === active ? 'class="active"' : ''}>${p.label}</a>`
  ).join('')
  const appID = getAppID()
  const userInfo = appID
    ? `<span class="nav-user">👤 ${appID} <a href="#" onclick="logout()">退出</a></span>`
    : `<span class="nav-user"><a href="auth.html">未登录</a></span>`
  return `<div class="nav">${links}${userInfo}</div>`
}

function logout() {
  clearAuth()
  location.href = 'auth.html'
}

// 通用 toast 提示
function toast(msg, type = 'info') {
  const el = document.createElement('div')
  el.className = 'toast toast-' + type
  el.textContent = msg
  document.body.appendChild(el)
  setTimeout(() => el.remove(), 3000)
}

// JSON 语法高亮
function highlight(json) {
  if (typeof json !== 'string') json = JSON.stringify(json, null, 2)
  return json
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/("(\\u[a-zA-Z0-9]{4}|\\[^u]|[^\\"])*"(\s*:)?|\b(true|false|null)\b|-?\d+(?:\.\d*)?(?:[eE][+\-]?\d+)?)/g, m => {
      let cls = 'json-num'
      if (/^"/.test(m)) cls = /:$/.test(m) ? 'json-key' : 'json-str'
      else if (/true|false/.test(m)) cls = 'json-bool'
      else if (/null/.test(m)) cls = 'json-null'
      return `<span class="${cls}">${m}</span>`
    })
}

// 公共 CSS（注入到 <head>）
const COMMON_CSS = `
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
  table { width: 100%; border-collapse: collapse; background: white; box-shadow: 0 2px 4px rgba(0,0,0,0.1); border-radius: 6px; overflow: hidden; }
  th, td { border: 1px solid #e0e0e0; padding: 10px 14px; text-align: left; font-size: 14px; }
  th { background: #1a73e8; color: white; font-weight: 600; }
  tr:nth-child(even) { background: #f8f9fa; }
  tr:hover { background: #e8f0fe; }
  .btn { display: inline-block; padding: 8px 16px; border-radius: 4px; border: none; cursor: pointer; font-size: 14px; font-weight: 500; transition: opacity .15s; }
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
  .form-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
  .code { background: #2c3e50; color: #ecf0f1; padding: 14px; border-radius: 5px; font-family: 'Courier New', monospace; font-size: 13px; line-height: 1.5; overflow-x: auto; white-space: pre-wrap; word-break: break-all; }
  .json-key  { color: #81d4fa; }
  .json-str  { color: #a5d6a7; }
  .json-num  { color: #ffcc80; }
  .json-bool { color: #ef9a9a; }
  .json-null { color: #b0bec5; }
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
  .tabs { display: flex; gap: 0; border-bottom: 2px solid #e0e0e0; margin-bottom: 20px; }
  .tab { padding: 10px 20px; cursor: pointer; font-size: 14px; font-weight: 500; color: #666; border-bottom: 2px solid transparent; margin-bottom: -2px; }
  .tab.active { color: #1a73e8; border-bottom-color: #1a73e8; }
  .tab-panel { display: none; }
  .tab-panel.active { display: block; }
  .split { display: grid; grid-template-columns: 320px 1fr; gap: 20px; }
  .skill-item { padding: 10px 14px; border-radius: 4px; cursor: pointer; border: 1px solid #e0e0e0; margin-bottom: 8px; background: white; }
  .skill-item:hover { border-color: #1a73e8; background: #e8f0fe; }
  .skill-item.selected { border-color: #1a73e8; background: #e8f0fe; }
  .skill-item .sid { font-size: 12px; color: #888; font-family: monospace; }
  .dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 6px; }
  .dot-green { background: #27ae60; }
  .dot-red   { background: #e74c3c; }
  .dot-gray  { background: #aaa; }
  .modal-overlay { display: none; position: fixed; inset: 0; background: rgba(0,0,0,.4); z-index: 1000; align-items: center; justify-content: center; }
  .modal-overlay.open { display: flex; }
  .modal { background: white; border-radius: 8px; padding: 24px; width: 600px; max-width: 95vw; max-height: 90vh; overflow-y: auto; box-shadow: 0 8px 32px rgba(0,0,0,.2); }
  .modal h3 { margin-bottom: 16px; color: #1a73e8; }
  .modal-footer { display: flex; justify-content: flex-end; gap: 10px; margin-top: 20px; }
  .loading { color: #888; font-size: 14px; padding: 20px; text-align: center; }
  .empty { color: #aaa; font-size: 14px; padding: 30px; text-align: center; }
`
