// Scheduled times cross the API as RFC 3339 with an explicit offset, so
// the backend never guesses a zone. The marketer works in the browser's
// local time; these helpers convert both ways.

const LOCAL_INPUT = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/

const pad = (n: number) => String(n).padStart(2, '0')

function offsetOf(d: Date): string {
  const minutes = -d.getTimezoneOffset()
  const sign = minutes < 0 ? '-' : '+'
  const abs = Math.abs(minutes)
  return `${sign}${pad(Math.floor(abs / 60))}:${pad(abs % 60)}`
}

// localInputToRFC3339 converts a datetime-local value ("2026-10-30T10:00")
// to RFC 3339 with the browser's offset on that date. It returns null for
// anything else, including a time that does not exist locally (skipped by
// a daylight-saving change).
export function localInputToRFC3339(value: string): string | null {
  const m = LOCAL_INPUT.exec(value)
  if (!m) return null
  const [y, mo, d, h, mi, s] = m.slice(1).map((p) => Number(p ?? 0)) as [number, number, number, number, number, number]
  const date = new Date(y, mo - 1, d, h, mi, s)
  if (
    date.getFullYear() !== y ||
    date.getMonth() !== mo - 1 ||
    date.getDate() !== d ||
    date.getHours() !== h ||
    date.getMinutes() !== mi
  ) {
    return null
  }
  return `${y}-${pad(mo)}-${pad(d)}T${pad(h)}:${pad(mi)}:${pad(s)}${offsetOf(date)}`
}

// formatWithOffset shows an RFC 3339 time in the browser's zone with the
// exact UTC offset, e.g. "30.10.2026 10:00:00 (UTC+03:00)".
export function formatWithOffset(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return `${d.toLocaleString()} (UTC${offsetOf(d)})`
}
