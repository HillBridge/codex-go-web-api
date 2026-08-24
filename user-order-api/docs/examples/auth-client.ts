/**
 * 浏览器端认证客户端示例。
 *
 * Access Token 只保存在当前页面的 JavaScript 内存；Refresh Token 由浏览器
 * 以 HttpOnly Cookie 方式保存。所有受保护请求应通过 apiFetch 发出。
 */

export class UnauthenticatedError extends Error {
  constructor() {
    super("登录已失效")
    this.name = "UnauthenticatedError"
  }
}

export class AuthClient {
  private accessToken: string | null = null
  private refreshPromise: Promise<string> | null = null

  constructor(private readonly baseURL = "/api/v1") {}

  /**
   * 应用启动时可主动调用；多个组件同时调用时只会请求一次 refresh 接口。
   */
  async restoreSession(): Promise<string> {
    return this.getAccessToken()
  }

  /**
   * 登录成功后写入返回的短期 Access Token。
   * Refresh Cookie 已由浏览器根据 Set-Cookie 自动保存。
   */
  setAccessToken(accessToken: string): void {
    this.accessToken = accessToken
  }

  /**
   * 在登录失效或主动退出后清除当前页面内存中的 Token。
   */
  clearAccessToken(): void {
    this.accessToken = null
  }

  /**
   * 受保护 API 的统一入口。
   * 遇到一次 401 时，刷新 Access Token 并仅重试原请求一次，避免死循环。
   */
  async apiFetch(path: string, options: RequestInit = {}): Promise<Response> {
    const token = await this.getAccessToken()
    let response = await this.send(path, token, options)

    if (response.status !== 401) {
      return response
    }

    const refreshedToken = await this.refreshAccessToken()
    response = await this.send(path, refreshedToken, options)

    if (response.status === 401) {
      this.clearAccessToken()
      throw new UnauthenticatedError()
    }

    return response
  }

  private async getAccessToken(): Promise<string> {
    if (this.accessToken) {
      return this.accessToken
    }
    return this.refreshAccessToken()
  }

  /**
   * 单飞刷新：所有并发调用都会 await 同一个 Promise，因此只请求一次 refresh。
   */
  private async refreshAccessToken(): Promise<string> {
    if (!this.refreshPromise) {
      this.refreshPromise = fetch(`${this.baseURL}/auth/refresh`, {
        method: "POST",
        credentials: "include",
      })
        .then(async (response) => {
          if (!response.ok) {
            this.clearAccessToken()
            throw new UnauthenticatedError()
          }

          const body = (await response.json()) as { accessToken: string }
          if (!body.accessToken) {
            throw new UnauthenticatedError()
          }

          this.accessToken = body.accessToken
          return body.accessToken
        })
        .finally(() => {
          this.refreshPromise = null
        })
    }

    return this.refreshPromise
  }

  private send(path: string, accessToken: string, options: RequestInit): Promise<Response> {
    const headers = new Headers(options.headers)
    headers.set("Authorization", `Bearer ${accessToken}`)

    return fetch(`${this.baseURL}${path}`, {
      ...options,
      credentials: "include",
      headers,
    })
  }
}

// 一个 SPA 中通常只创建一个实例，并由所有业务模块复用：
// export const authClient = new AuthClient()
// const response = await authClient.apiFetch("/orders")
