import { getJSON } from './client'

export interface HealthResponse {
  status: 'ok' | 'unavailable'
}

function isHealthResponse(value: unknown): value is HealthResponse {
  if (typeof value !== 'object' || value === null) return false
  const status = (value as Record<string, unknown>).status
  return status === 'ok' || status === 'unavailable'
}

export async function fetchHealth(signal?: AbortSignal): Promise<HealthResponse> {
  const body = await getJSON('/healthz', signal)
  if (!isHealthResponse(body)) {
    throw new Error('Unexpected health response shape')
  }
  return body
}
