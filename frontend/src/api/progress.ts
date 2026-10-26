// Campaign delivery progress: types, response parsing, the JSON snapshot
// call, and the live event stream URL. Every snapshot is the full state;
// the dashboard replaces what it shows with each one.

import { ApiError, API_BASE, requestJSON } from './client'
import { CHANNELS, STATUSES, type CampaignStatus, type Channel } from './campaigns'
import { asArray, asNumber, asObject, asOneOf, asString, nullable, ShapeError } from './guards'

export const DELIVERY_STATUSES = ['queued', 'processing', 'sent', 'failed', 'retrying'] as const
export type DeliveryStatus = (typeof DELIVERY_STATUSES)[number]

export type StatusCounts = Record<DeliveryStatus | 'total', number>

export interface ChannelProgress {
  channel: Channel
  counts: StatusCounts
  sentRecently: number
  percentDone: number
}

export interface FailureCount {
  code: string
  count: number
}

export interface Progress {
  campaignId: string
  campaignStatus: CampaignStatus
  // scheduledAt is the release time of a campaign approved for later.
  scheduledAt: string | null
  channels: ChannelProgress[]
  totals: StatusCounts
  percentDone: number
  failures: FailureCount[]
  throughput: { windowSeconds: number; sent: number; perSecond: number }
  generatedAt: string
}

function parseCounts(v: unknown, path: string): StatusCounts {
  const o = asObject(v, path)
  return {
    queued: asNumber(o.queued, `${path}.queued`),
    processing: asNumber(o.processing, `${path}.processing`),
    sent: asNumber(o.sent, `${path}.sent`),
    failed: asNumber(o.failed, `${path}.failed`),
    retrying: asNumber(o.retrying, `${path}.retrying`),
    total: asNumber(o.total, `${path}.total`),
  }
}

// parseProgress validates an untrusted snapshot; it throws ShapeError.
export function parseProgress(v: unknown): Progress {
  const o = asObject(v, 'progress')
  const t = asObject(o.throughput, 'throughput')
  return {
    campaignId: asString(o.campaign_id, 'campaign_id'),
    campaignStatus: asOneOf(o.campaign_status, STATUSES, 'campaign_status'),
    scheduledAt: nullable(o.scheduled_at, (v) => asString(v, 'scheduled_at')),
    channels: asArray(o.channels, 'channels').map((c, i) => {
      const p = `channels[${i}]`
      const co = asObject(c, p)
      return {
        channel: asOneOf(co.channel, CHANNELS, `${p}.channel`),
        counts: parseCounts(co, p),
        sentRecently: asNumber(co.sent_recently, `${p}.sent_recently`),
        percentDone: Math.min(100, Math.max(0, asNumber(co.percent_done, `${p}.percent_done`))),
      }
    }),
    totals: parseCounts(o.totals, 'totals'),
    percentDone: Math.min(100, Math.max(0, asNumber(o.percent_done, 'percent_done'))),
    failures: asArray(o.failures, 'failures').map((f, i) => {
      const fo = asObject(f, `failures[${i}]`)
      return { code: asString(fo.code, `failures[${i}].code`), count: asNumber(fo.count, `failures[${i}].count`) }
    }),
    throughput: {
      windowSeconds: asNumber(t.window_seconds, 'throughput.window_seconds'),
      sent: asNumber(t.sent, 'throughput.sent'),
      perSecond: asNumber(t.per_second, 'throughput.per_second'),
    },
    generatedAt: asString(o.generated_at, 'generated_at'),
  }
}

const base = '/v1/campaigns'

export async function getProgress(id: string, signal?: AbortSignal): Promise<Progress> {
  const body = await requestJSON('GET', `${base}/${encodeURIComponent(id)}/progress`, undefined, { signal })
  try {
    return parseProgress(body)
  } catch (err) {
    if (err instanceof ShapeError) throw new ApiError(0, 'invalid_response', 'The API returned an unexpected response.')
    throw err
  }
}

// progressEventsURL is the live stream for EventSource. Events: "snapshot"
// and a final "complete" (completed or expired), both carrying a full
// Progress.
export function progressEventsURL(id: string): string {
  return `${API_BASE}${base}/${encodeURIComponent(id)}/events`
}
