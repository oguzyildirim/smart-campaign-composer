import { ApiError } from '../api/client'

// Hints for error codes where the backend message alone does not say what
// to do next.
const hints: Record<string, string> = {
  version_conflict: 'Reload the page to see the latest version.',
  content_changed: 'Review the latest content, then approve again.',
  generation_in_progress: 'Wait for the current generation to finish.',
  invalid_output: 'Try again, or rephrase the brief.',
  upstream_unavailable: 'Try again in a moment.',
  timeout: 'Try again in a moment.',
  network: 'Check that the API is running.',
}

// describeError returns display text for any thrown value. Backend
// messages are client-safe and rendered as plain text.
export function describeError(err: unknown): string {
  if (err instanceof ApiError) {
    const hint = hints[err.code]
    return hint ? `${err.message} ${hint}` : err.message
  }
  return 'Something went wrong.'
}

export function isAbort(err: unknown): boolean {
  return err instanceof DOMException && err.name === 'AbortError'
}
