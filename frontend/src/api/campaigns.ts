// Campaign API: types, response parsing, and calls. The backend owns
// campaign status, validation, rendering, and approval; this module only
// transports and type-checks what it says.

import { ApiError, errorFromResponse, requestJSON, API_BASE, type FieldError } from './client'
import {
  asArray,
  asBoolean,
  asNumber,
  asObject,
  asOneOf,
  asString,
  asStringArray,
  nullable,
  ShapeError,
  type Obj,
} from './guards'
import { readSSE } from './sse'

export const STATUSES = [
  'draft',
  'audience_ready',
  'drafts_ready',
  'scheduled',
  'release_failed',
  'expired',
  'approved',
  'sending',
  'completed',
] as const
export type CampaignStatus = (typeof STATUSES)[number]

// Statuses after human approval to send now, or after a scheduled
// campaign's release, as reported by the backend. Deliveries exist.
const APPROVED_STATUSES: readonly CampaignStatus[] = ['approved', 'sending', 'completed']

// Statuses of a campaign approved for a later time that was not sent; a
// cancel returns it to drafts.
const SCHEDULE_STATUSES: readonly CampaignStatus[] = ['scheduled', 'release_failed', 'expired']

export function isApproved(status: CampaignStatus): boolean {
  return APPROVED_STATUSES.includes(status)
}

export function isScheduleStatus(status: CampaignStatus): boolean {
  return SCHEDULE_STATUSES.includes(status)
}

// holdsApproval: the campaign carries a human approval, so its content
// can no longer change.
export function holdsApproval(status: CampaignStatus): boolean {
  return isApproved(status) || isScheduleStatus(status)
}

export const CHANNELS = ['sms', 'email'] as const
export type Channel = (typeof CHANNELS)[number]

export const DRAFT_FIELDS = ['sms_body', 'email_subject', 'email_body'] as const
export type DraftField = (typeof DRAFT_FIELDS)[number]

// Input caps for form usability only (counters, maxlength); the backend
// validates every value again and has the final say. Message field
// limits are not copied here: they arrive in each validation response.
export const MAX_BRIEF_CHARS = 1000
export const MAX_APPROVER_CHARS = 100

export interface Counts {
  total: number
  sms: number
  email: number
}

export interface Drafts {
  sms_body: string
  email_subject: string
  email_body: string
}

export interface Validation {
  valid: boolean
  errors: FieldError[]
  worstCaseLengths: Record<DraftField, number>
  limits: Record<DraftField, number>
}

export interface Audience {
  summary: string[]
  explanation: string
  unsupportedCriteria: string[]
  counts: Counts
}

// ReleaseFailure is why a scheduled campaign was not sent.
export interface ReleaseFailure {
  code: string
  error: string
  details: FieldError[]
}

export interface Campaign {
  id: string
  brief: string
  status: CampaignStatus
  audience: Audience
  channels: Channel[]
  drafts: Drafts | null
  validation: Validation | null
  version: number
  contentHash: string
  generating: boolean
  approvedBy: string | null
  approvedAt: string | null
  scheduledAt: string | null
  releaseFailure: ReleaseFailure | null
  createdAt: string
  updatedAt: string
}

export interface CampaignSummary {
  id: string
  brief: string
  status: CampaignStatus
  counts: Counts
  channels: Channel[]
  approvedAt: string | null
  scheduledAt: string | null
  createdAt: string
}

export interface RenderedField {
  field: string
  text: string
  length: number
  limit: number
  overLimit: boolean
}

export interface SamplePreview {
  userId: number
  firstName: string
  city: string
  fields: RenderedField[]
}

export interface ChannelPreview {
  channel: Channel
  recipients: number
  longestFirstName: number
  longestCity: number
  samples: SamplePreview[]
}

export interface Preview {
  version: number
  contentHash: string
  validation: Validation
  channels: ChannelPreview[]
}

function parseCounts(v: unknown, path: string): Counts {
  const o = asObject(v, path)
  return {
    total: asNumber(o.total, `${path}.total`),
    sms: asNumber(o.sms, `${path}.sms`),
    email: asNumber(o.email, `${path}.email`),
  }
}

function parseDrafts(v: unknown, path: string): Drafts {
  const o = asObject(v, path)
  return {
    sms_body: asString(o.sms_body, `${path}.sms_body`),
    email_subject: asString(o.email_subject, `${path}.email_subject`),
    email_body: asString(o.email_body, `${path}.email_body`),
  }
}

function parseFieldErrors(v: unknown, path: string): FieldError[] {
  return asArray(v, path).map((e, i) => {
    const o = asObject(e, `${path}[${i}]`)
    return { path: asString(o.path, `${path}[${i}].path`), message: asString(o.message, `${path}[${i}].message`) }
  })
}

function parseValidation(v: unknown, path: string): Validation {
  const o = asObject(v, path)
  const wc = asObject(o.worst_case_lengths, `${path}.worst_case_lengths`)
  const limits = asObject(o.limits, `${path}.limits`)
  return {
    valid: asBoolean(o.valid, `${path}.valid`),
    errors: parseFieldErrors(o.errors, `${path}.errors`),
    worstCaseLengths: {
      sms_body: asNumber(wc.sms_body, `${path}.worst_case_lengths.sms_body`),
      email_subject: asNumber(wc.email_subject, `${path}.worst_case_lengths.email_subject`),
      email_body: asNumber(wc.email_body, `${path}.worst_case_lengths.email_body`),
    },
    limits: {
      sms_body: asNumber(limits.sms_body, `${path}.limits.sms_body`),
      email_subject: asNumber(limits.email_subject, `${path}.limits.email_subject`),
      email_body: asNumber(limits.email_body, `${path}.limits.email_body`),
    },
  }
}

function parseChannels(v: unknown, path: string): Channel[] {
  return asArray(v, path).map((c, i) => asOneOf(c, CHANNELS, `${path}[${i}]`))
}

export function parseCampaign(v: unknown): Campaign {
  const o = asObject(v, 'campaign')
  const a = asObject(o.audience, 'audience')
  return {
    id: asString(o.id, 'id'),
    brief: asString(o.brief, 'brief'),
    status: asOneOf(o.status, STATUSES, 'status'),
    audience: {
      summary: asStringArray(a.summary, 'audience.summary'),
      explanation: asString(a.explanation, 'audience.explanation'),
      unsupportedCriteria: asStringArray(a.unsupported_criteria, 'audience.unsupported_criteria'),
      counts: parseCounts(a.counts, 'audience.counts'),
    },
    channels: parseChannels(o.channels, 'channels'),
    drafts: nullable(o.drafts, (d) => parseDrafts(d, 'drafts')),
    validation: nullable(o.validation, (d) => parseValidation(d, 'validation')),
    version: asNumber(o.version, 'version'),
    contentHash: asString(o.content_hash, 'content_hash'),
    generating: asBoolean(o.generating, 'generating'),
    approvedBy: nullable(o.approved_by, (s) => asString(s, 'approved_by')),
    approvedAt: nullable(o.approved_at, (s) => asString(s, 'approved_at')),
    scheduledAt: nullable(o.scheduled_at, (s) => asString(s, 'scheduled_at')),
    releaseFailure: nullable(o.release_failure, (f) => {
      const fo = asObject(f, 'release_failure')
      return {
        code: asString(fo.code, 'release_failure.code'),
        error: asString(fo.error, 'release_failure.error'),
        details: parseFieldErrors(fo.details, 'release_failure.details'),
      }
    }),
    createdAt: asString(o.created_at, 'created_at'),
    updatedAt: asString(o.updated_at, 'updated_at'),
  }
}

function parseSummary(v: unknown, i: number): CampaignSummary {
  const p = `campaigns[${i}]`
  const o = asObject(v, p)
  return {
    id: asString(o.id, `${p}.id`),
    brief: asString(o.brief, `${p}.brief`),
    status: asOneOf(o.status, STATUSES, `${p}.status`),
    counts: parseCounts(o.counts, `${p}.counts`),
    channels: parseChannels(o.channels, `${p}.channels`),
    approvedAt: nullable(o.approved_at, (s) => asString(s, `${p}.approved_at`)),
    scheduledAt: nullable(o.scheduled_at, (s) => asString(s, `${p}.scheduled_at`)),
    createdAt: asString(o.created_at, `${p}.created_at`),
  }
}

function parsePreview(v: unknown): Preview {
  const o = asObject(v, 'preview')
  return {
    version: asNumber(o.version, 'version'),
    contentHash: asString(o.content_hash, 'content_hash'),
    validation: parseValidation(o.validation, 'validation'),
    channels: asArray(o.channels, 'channels').map((c, i): ChannelPreview => {
      const p = `channels[${i}]`
      const co = asObject(c, p)
      const longest = asObject(co.longest_values, `${p}.longest_values`)
      return {
        channel: asOneOf(co.channel, CHANNELS, `${p}.channel`),
        recipients: asNumber(co.recipients, `${p}.recipients`),
        longestFirstName: asNumber(longest.first_name ?? 0, `${p}.longest_values.first_name`),
        longestCity: asNumber(longest.city ?? 0, `${p}.longest_values.city`),
        samples: asArray(co.samples, `${p}.samples`).map((s, j): SamplePreview => {
          const sp = `${p}.samples[${j}]`
          const so = asObject(s, sp)
          return {
            userId: asNumber(so.user_id, `${sp}.user_id`),
            firstName: asString(so.first_name, `${sp}.first_name`),
            city: asString(so.city, `${sp}.city`),
            fields: asArray(so.fields, `${sp}.fields`).map((f, k): RenderedField => {
              const fp = `${sp}.fields[${k}]`
              const fo = asObject(f, fp)
              return {
                field: asString(fo.field, `${fp}.field`),
                text: asString(fo.text, `${fp}.text`),
                length: asNumber(fo.length, `${fp}.length`),
                limit: asNumber(fo.limit, `${fp}.limit`),
                overLimit: asBoolean(fo.over_limit, `${fp}.over_limit`),
              }
            }),
          }
        }),
      }
    }),
  }
}

// parsed turns a ShapeError into an ApiError so views handle one type.
function parsed<T>(parse: () => T): T {
  try {
    return parse()
  } catch (err) {
    if (err instanceof ShapeError) {
      throw new ApiError(0, 'invalid_response', 'The API returned an unexpected response.')
    }
    throw err
  }
}

const base = '/v1/campaigns'

// Creation runs AI segment extraction, which can take a while.
const CREATE_TIMEOUT_MS = 120_000
const WRITE_TIMEOUT_MS = 20_000

export async function listCampaigns(signal?: AbortSignal): Promise<CampaignSummary[]> {
  const body = await requestJSON('GET', base, undefined, { signal })
  return parsed(() => asArray(asObject(body, 'list').campaigns, 'campaigns').map(parseSummary))
}

export async function getCampaign(id: string, signal?: AbortSignal): Promise<Campaign> {
  const body = await requestJSON('GET', `${base}/${encodeURIComponent(id)}`, undefined, { signal })
  return parsed(() => parseCampaign(body))
}

export async function createCampaign(brief: string, signal?: AbortSignal): Promise<Campaign> {
  const body = await requestJSON('POST', base, { brief }, { signal, timeoutMs: CREATE_TIMEOUT_MS })
  return parsed(() => parseCampaign(body))
}

export interface EditInput extends Drafts {
  version: number
  channels: Channel[]
}

export async function editCampaign(id: string, input: EditInput, signal?: AbortSignal): Promise<Campaign> {
  const body = await requestJSON('PATCH', `${base}/${encodeURIComponent(id)}`, input, {
    signal,
    timeoutMs: WRITE_TIMEOUT_MS,
  })
  return parsed(() => parseCampaign(body))
}

export async function previewCampaign(id: string, signal?: AbortSignal): Promise<Preview> {
  const body = await requestJSON('GET', `${base}/${encodeURIComponent(id)}/preview`, undefined, { signal })
  return parsed(() => parsePreview(body))
}

// ApproveInput approves the reviewed version and hash; scheduled_at
// (RFC 3339 with offset) approves for release at that time instead.
export interface ApproveInput {
  version: number
  content_hash: string
  approved_by: string
  scheduled_at?: string
}

export async function approveCampaign(id: string, input: ApproveInput, signal?: AbortSignal): Promise<Campaign> {
  const body = await requestJSON('POST', `${base}/${encodeURIComponent(id)}/approve`, input, {
    signal,
    timeoutMs: WRITE_TIMEOUT_MS,
  })
  return parsed(() => parseCampaign(body))
}

export interface RescheduleInput {
  version: number
  content_hash: string
  scheduled_at: string
  actor: string
}

export async function rescheduleCampaign(id: string, input: RescheduleInput, signal?: AbortSignal): Promise<Campaign> {
  const body = await requestJSON('POST', `${base}/${encodeURIComponent(id)}/reschedule`, input, {
    signal,
    timeoutMs: WRITE_TIMEOUT_MS,
  })
  return parsed(() => parseCampaign(body))
}

export interface CancelInput {
  version: number
  content_hash: string
  actor: string
}

export async function cancelCampaign(id: string, input: CancelInput, signal?: AbortSignal): Promise<Campaign> {
  const body = await requestJSON('POST', `${base}/${encodeURIComponent(id)}/cancel`, input, {
    signal,
    timeoutMs: WRITE_TIMEOUT_MS,
  })
  return parsed(() => parseCampaign(body))
}

export interface DraftDelta {
  attempt: number
  field: DraftField
  text: string
}

export interface DraftsDone {
  status: CampaignStatus
  drafts: Drafts
  validation: Validation
  version: number
  contentHash: string
}

export interface DraftStreamHandlers {
  onDelta: (delta: DraftDelta) => void
}

function parseDelta(o: Obj): DraftDelta {
  return {
    attempt: asNumber(o.attempt, 'delta.attempt'),
    field: asOneOf(o.field, DRAFT_FIELDS, 'delta.field'),
    text: asString(o.text, 'delta.text'),
  }
}

function parseDone(o: Obj): DraftsDone {
  return {
    status: asOneOf(o.status, STATUSES, 'done.status'),
    drafts: parseDrafts(o.drafts, 'done.drafts'),
    validation: parseValidation(o.validation, 'done.validation'),
    version: asNumber(o.version, 'done.version'),
    contentHash: asString(o.content_hash, 'done.content_hash'),
  }
}

// streamDrafts starts draft generation and resolves with the validated,
// stored drafts. Deltas are display-only. It rejects with an ApiError for
// a refused start (e.g. 409), an error event, or a stream that ends early;
// aborting signal cancels generation on the server.
export async function streamDrafts(
  id: string,
  handlers: DraftStreamHandlers,
  signal: AbortSignal,
): Promise<DraftsDone> {
  let response: Response
  try {
    response = await fetch(`${API_BASE}${base}/${encodeURIComponent(id)}/drafts`, {
      method: 'POST',
      headers: { Accept: 'text/event-stream' },
      signal,
    })
  } catch (err) {
    if (signal.aborted) throw err
    throw new ApiError(0, 'network', 'The API could not be reached.')
  }
  if (!response.ok) throw await errorFromResponse(response)
  if (!response.body) throw new ApiError(0, 'invalid_response', 'The draft stream is empty.')

  let result: DraftsDone | null = null
  let failure: ApiError | null = null
  await readSSE(response.body, ({ event, data }) => {
      if (result || failure) return
      let payload: Obj
      try {
        payload = asObject(JSON.parse(data), event)
      } catch {
        throw new ShapeError(event)
      }
      switch (event) {
        case 'delta':
          handlers.onDelta(parseDelta(payload))
          break
        case 'done':
          result = parseDone(payload)
          break
        case 'error':
          failure = new ApiError(
            response.status,
            asString(payload.code, 'error.code'),
            asString(payload.message, 'error.message'),
          )
          break
      }
  }).catch((err: unknown) => {
    if (err instanceof ShapeError) throw new ApiError(0, 'invalid_response', 'The draft stream was malformed.')
    throw err
  })
  if (failure) throw failure
  if (!result) throw new ApiError(0, 'stream_ended', 'The draft stream ended before the drafts were ready.')
  return result
}
