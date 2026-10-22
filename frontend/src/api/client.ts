// Shared HTTP client. All API calls go through here so base URL, timeouts,
// and error handling stay consistent. Responses are untrusted: callers must
// validate their shape before use.

export const API_BASE = '/api'
const DEFAULT_TIMEOUT_MS = 5000

export class ApiError extends Error {
  readonly status: number

  constructor(status: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

export async function getJSON(path: string, signal?: AbortSignal): Promise<unknown> {
  const timeout = AbortSignal.timeout(DEFAULT_TIMEOUT_MS)
  const response = await fetch(`${API_BASE}${path}`, {
    headers: { Accept: 'application/json' },
    signal: signal ? AbortSignal.any([signal, timeout]) : timeout,
  })
  if (!response.ok) {
    throw new ApiError(response.status, `Request failed with status ${response.status}`)
  }
  return response.json()
}
