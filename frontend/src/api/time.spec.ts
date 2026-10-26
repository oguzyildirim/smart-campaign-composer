import { afterEach, describe, expect, it } from 'vitest'
import { setTimeZone } from '../test/helpers'
import { formatWithOffset, localInputToRFC3339 } from './time'

let restore = () => {}

function zone(tz: string) {
  restore()
  restore = setTimeZone(tz)
}

afterEach(() => {
  restore()
  restore = () => {}
})

describe('localInputToRFC3339', () => {
  it('adds the browser offset for that date', () => {
    zone('Europe/Istanbul')
    expect(localInputToRFC3339('2026-10-30T10:00')).toBe('2026-10-30T10:00:00+03:00')
    zone('America/New_York')
    expect(localInputToRFC3339('2026-10-30T10:00')).toBe('2026-10-30T10:00:00-04:00') // EDT
    expect(localInputToRFC3339('2026-11-02T10:00')).toBe('2026-11-02T10:00:00-05:00') // EST after the change
    zone('Asia/Kolkata')
    expect(localInputToRFC3339('2026-10-30T10:00:30')).toBe('2026-10-30T10:00:30+05:30')
    zone('UTC')
    expect(localInputToRFC3339('2026-10-30T10:00')).toBe('2026-10-30T10:00:00+00:00')
  })

  it('rejects input that is not a local date and time', () => {
    zone('Europe/Istanbul')
    for (const bad of ['', '2026-10-30', '10:00', '2026-10-30T10:00Z', '2026-13-30T10:00', 'tomorrow']) {
      expect(localInputToRFC3339(bad), bad).toBeNull()
    }
  })

  it('rejects a local time skipped by a daylight-saving change', () => {
    zone('America/New_York')
    expect(localInputToRFC3339('2027-03-14T02:30')).toBeNull()
  })
})

describe('formatWithOffset', () => {
  it('shows the time in the browser zone with its exact offset', () => {
    zone('Europe/Istanbul')
    expect(formatWithOffset('2026-10-30T07:00:00Z')).toMatch(/10:00.*\(UTC\+03:00\)$/)
    zone('America/New_York')
    expect(formatWithOffset('2026-10-30T10:00:00+03:00')).toMatch(/3:00.*\(UTC-04:00\)$/)
  })

  it('returns unparsable input unchanged', () => {
    expect(formatWithOffset('soon')).toBe('soon')
  })
})
