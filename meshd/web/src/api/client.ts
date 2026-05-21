// api/client.ts —— 浏览器侧 axios 实例。
//
// 鉴权模型：
//   1. 浏览器启动时调本机 meshd `/api/config` 拿 gateway_url（避免硬编码）
//   2. 用户名/密码登录调 Gateway /v1/admin/auth/login → 拿 user JWT，存 localStorage
//   3. 之后所有业务请求 axios baseURL = ${gateway_url}/v1/admin，
//      Authorization: Bearer ${localStorage.token}
//   4. 401 → 清 localStorage + 跳 /login
//
// 本机 meshd API 单独一个 instance（`meshdApi`），跟 Gateway 解耦。

import axios, { AxiosInstance } from 'axios'

const TOKEN_KEY = 'mesh_user_jwt'
const UID_KEY = 'mesh_user_uid'
const USERNAME_KEY = 'mesh_user_username'

/** 当前登录态（同步从 localStorage 读，没有 = 未登录）。 */
export function currentToken(): string | null {
  return typeof window !== 'undefined' ? window.localStorage.getItem(TOKEN_KEY) : null
}

export function currentUID(): number | null {
  if (typeof window === 'undefined') return null
  const v = window.localStorage.getItem(UID_KEY)
  return v ? parseInt(v, 10) : null
}

export function currentUsername(): string | null {
  return typeof window !== 'undefined' ? window.localStorage.getItem(USERNAME_KEY) : null
}

export function setSession(token: string, uid: number, username?: string): void {
  window.localStorage.setItem(TOKEN_KEY, token)
  window.localStorage.setItem(UID_KEY, String(uid))
  if (username) window.localStorage.setItem(USERNAME_KEY, username)
}

export function clearSession(): void {
  window.localStorage.removeItem(TOKEN_KEY)
  window.localStorage.removeItem(UID_KEY)
  window.localStorage.removeItem(USERNAME_KEY)
}

// ─── meshd 本机 API ─────────────────────────────────────────────────────
// 同源（浏览器和 meshd 都在 :7878），cookie 自动带，无需 Authorization
export const meshdApi: AxiosInstance = axios.create({
  baseURL: '/api',
  withCredentials: true,
})

// ─── Gateway 业务 API ───────────────────────────────────────────────────
// baseURL 在 bootstrap() 之后才确定。在 bootstrap 完成前调 api 会抛错，
// 应该等 main.tsx 完成 bootstrap 之后才 mount React。

const api: AxiosInstance = axios.create({})

/** bootstrap 一次：拉 Gateway URL，配 axios baseURL + 拦截器。 */
export async function bootstrapApiClient(): Promise<{ gateway_url: string }> {
  const res = await meshdApi.get<{ gateway_url: string; version: string }>('/config')
  const baseURL = `${res.data.gateway_url.replace(/\/+$/, '')}/v1/admin`
  api.defaults.baseURL = baseURL

  // request: 自动注入 Authorization
  api.interceptors.request.use((config) => {
    const tok = currentToken()
    if (tok) {
      config.headers = config.headers ?? {}
      ;(config.headers as any).Authorization = `Bearer ${tok}`
    }
    return config
  })

  // response: 401 时清 localStorage + 跳 /login
  api.interceptors.response.use(
    (resp) => resp,
    (err) => {
      if (err.response?.status === 401 && typeof window !== 'undefined') {
        clearSession()
        if (window.location.pathname !== '/login') {
          window.location.href = '/login'
        }
      }
      return Promise.reject(err)
    },
  )

  return res.data
}

export default api
