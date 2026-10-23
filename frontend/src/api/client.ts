// Shared HTTP client. All API calls go through here so base URL, timeouts,
// and error handling stay consistent. Responses are untrusted: callers must
// validate their shape before use.

export const API_BASE = '/api'
const DEFAULT_TIMEOUT_MS = 5000

export interface FieldError {
  path: string
  message: string
}

// ApiError is a failed request. code is the backend's stable error code
// ("network", "timeout", or "invalid_response" for client-side failures);
// message is safe to display as text.
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly details: FieldError[]
  readonly unsupportedCriteria: string[]

  constructor(
    status: number,
    code: string,
    message: string,
    details: FieldError[] = [],
    unsupportedCriteria: string[] = [],
  ) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.details = details
    this.unsupportedCriteria = unsupportedCriteria
  }
}

export interface RequestOptions {
  signal?: AbortSignal
  timeoutMs?: number
}

function strings(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((v): v is string => typeof v === 'string') : []
}

function fieldErrors(value: unknown): FieldError[] {
  if (!Array.isArray(value)) return []
  return value.flatMap((v: unknown) => {
    if (typeof v !== 'object' || v === null) return []
    const { path, message } = v as Record<string, unknown>
    return typeof path === 'string' && typeof message === 'string' ? [{ path, message }] : []
  })
}

// errorFromResponse reads the backend's error body; anything unexpected
// falls back to a generic message.
export async function errorFromResponse(response: Response): Promise<ApiError> {
  let body: unknown = null
  try {
    body = await response.json()
  } catch {
    // Not JSON: keep the generic message.
  }
  const b = typeof body === 'object' && body !== null ? (body as Record<string, unknown>) : {}
  const code = typeof b.code === 'string' ? b.code : 'http_error'
  const message = typeof b.error === 'string' ? b.error : `Request failed with status ${response.status}`
  return new ApiError(response.status, code, message, fieldErrors(b.details), strings(b.unsupported_criteria))
}

// signalFor combines the caller's signal with a timeout.
export function signalFor(options: RequestOptions): AbortSignal {
  const timeout = AbortSignal.timeout(options.timeoutMs ?? DEFAULT_TIMEOUT_MS)
  return options.signal ? AbortSignal.any([options.signal, timeout]) : timeout
}

// requestJSON sends an optional JSON body and returns the parsed JSON
// response. Network failures and timeouts become ApiErrors; an abort by
// the caller is rethrown unchanged so callers can ignore it.
export async function requestJSON(
  method: string,
  path: string,
  body?: unknown,
  options: RequestOptions = {},
): Promise<unknown> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  let response: Response
  try {
    response = await fetch(`${API_BASE}${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: signalFor(options),
    })
  } catch (err) {
    if (options.signal?.aborted) throw err
    if (err instanceof DOMException && err.name === 'TimeoutError') {
      throw new ApiError(0, 'timeout', 'The request timed out.')
    }
    throw new ApiError(0, 'network', 'The API could not be reached.')
  }
  if (!response.ok) throw await errorFromResponse(response)
  try {
    return await response.json()
  } catch {
    throw new ApiError(response.status, 'invalid_response', 'The API returned an unreadable response.')
  }
}

export async function getJSON(path: string, signal?: AbortSignal): Promise<unknown> {
  return requestJSON('GET', path, undefined, { signal })
}
