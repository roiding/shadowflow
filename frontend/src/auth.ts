const TOKEN_KEY = 'shadowflow.api-token'
export const UNAUTHORIZED_EVENT = 'shadowflow:unauthorized'

export function getToken(): string {
  return sessionStorage.getItem(TOKEN_KEY) ?? ''
}

export function setToken(value: string): void {
  sessionStorage.setItem(TOKEN_KEY, value.trim())
}

export function clearToken(): void {
  sessionStorage.removeItem(TOKEN_KEY)
}

export function notifyUnauthorized(): void {
  clearToken()
  window.dispatchEvent(new CustomEvent(UNAUTHORIZED_EVENT))
}
