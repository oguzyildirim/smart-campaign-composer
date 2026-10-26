// Test helpers: fetch stubbing, controllable streams, and API fixtures in
// the backend's wire format.
import { vi } from 'vitest'

export function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

export function errorResponse(status: number, code: string, error: string, extra: Record<string, unknown> = {}): Response {
  return jsonResponse({ error, code, ...extra }, status)
}

// ControlledStream is a response body the test feeds chunk by chunk.
export interface ControlledStream {
  response: Response
  push: (text: string) => void
  event: (name: string, data: unknown) => void
  close: () => void
}

export function controlledStream(): ControlledStream {
  const encoder = new TextEncoder()
  let ctrl!: ReadableStreamDefaultController<Uint8Array>
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      ctrl = c
    },
  })
  const push = (text: string) => ctrl.enqueue(encoder.encode(text))
  return {
    response: new Response(body, { status: 200, headers: { 'Content-Type': 'text/event-stream' } }),
    push,
    event: (name, data) => push(`event: ${name}\ndata: ${JSON.stringify(data)}\n\n`),
    close: () => ctrl.close(),
  }
}

export interface RecordedRequest {
  method: string
  url: string
  body: unknown
  signal: AbortSignal | null
}

type Handler = (req: RecordedRequest) => Response | Promise<Response>

// stubFetch routes requests by "METHOD path" and records them. Unrouted
// requests fail the test loudly.
export function stubFetch(routes: Record<string, Handler>) {
  const requests: RecordedRequest[] = []
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input.toString()
    const method = init?.method ?? 'GET'
    const body = typeof init?.body === 'string' ? (JSON.parse(init.body) as unknown) : undefined
    const req: RecordedRequest = { method, url, body, signal: init?.signal ?? null }
    requests.push(req)
    const handler = routes[`${method} ${url}`]
    if (!handler) throw new Error(`unexpected request ${method} ${url}`)
    return handler(req)
  })
  vi.stubGlobal('fetch', fetchMock)
  return { requests, fetchMock }
}

export const CAMPAIGN_ID = '0196f0a2-7c1e-7b3a-9d2e-3f4a5b6c7d8e'

export const drafts = {
  sms_body: 'Merhaba {{first_name}}, sepetindeki ürünler seni bekliyor!',
  email_subject: '{{first_name}}, sepetin seni bekliyor',
  email_body: 'Merhaba {{first_name}},\n\nSepetin seni bekliyor.',
}

const validValidation = {
  valid: true,
  errors: [],
  worst_case_lengths: { sms_body: 80, email_subject: 50, email_body: 70 },
  limits: { sms_body: 160, email_subject: 80, email_body: 2000 },
}

export function campaignJSON(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    id: CAMPAIGN_ID,
    brief: 'Son 30 günde sepetine ürün ekleyip alışveriş yapmayan İstanbul kullanıcıları',
    status: 'drafts_ready',
    audience: {
      rule: { version: 1, conditions: [] },
      summary: ['City is İstanbul', 'Added to cart in the last 30 days'],
      explanation: 'Users matching: city İstanbul.',
      unsupported_criteria: [],
      counts: { total: 389, sms: 262, email: 246 },
    },
    channels: ['sms', 'email'],
    drafts,
    validation: validValidation,
    version: 2,
    content_hash: 'a'.repeat(64),
    generating: false,
    approved_by: null,
    approved_at: null,
    scheduled_at: null,
    release_failure: null,
    created_at: '2026-10-22T12:00:00Z',
    updated_at: '2026-10-22T12:00:00Z',
    ...overrides,
  }
}

export function previewJSON(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    version: 2,
    content_hash: 'a'.repeat(64),
    validation: validValidation,
    channels: [
      {
        channel: 'sms',
        recipients: 262,
        longest_values: { first_name: 30, city: 8 },
        samples: [
          {
            user_id: 7,
            first_name: 'Ayşe',
            city: 'İstanbul',
            fields: [
              { field: 'sms.body', text: 'Merhaba Ayşe, sepetindeki ürünler seni bekliyor!', length: 48, limit: 160, over_limit: false },
            ],
          },
        ],
      },
    ],
    ...overrides,
  }
}

// FakeEventSource stands in for the browser's EventSource. Tests drive it
// with open(), emit(), and error(); instances lists every one created.
export class FakeEventSource {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSED = 2
  static instances: FakeEventSource[] = []

  readonly url: string
  readyState = FakeEventSource.CONNECTING
  onopen: ((ev: Event) => void) | null = null
  onerror: ((ev: Event) => void) | null = null
  private listeners = new Map<string, Set<(ev: MessageEvent) => void>>()

  constructor(url: string | URL) {
    this.url = url.toString()
    FakeEventSource.instances.push(this)
  }

  addEventListener(name: string, fn: (ev: MessageEvent) => void) {
    const set = this.listeners.get(name) ?? new Set()
    set.add(fn)
    this.listeners.set(name, set)
  }

  removeEventListener(name: string, fn: (ev: MessageEvent) => void) {
    this.listeners.get(name)?.delete(fn)
  }

  close() {
    this.readyState = FakeEventSource.CLOSED
  }

  get closed(): boolean {
    return this.readyState === FakeEventSource.CLOSED
  }

  open() {
    this.readyState = FakeEventSource.OPEN
    this.onopen?.(new Event('open'))
  }

  emit(name: string, data: unknown, lastEventId = '') {
    const ev = new MessageEvent(name, { data: typeof data === 'string' ? data : JSON.stringify(data), lastEventId })
    this.listeners.get(name)?.forEach((fn) => fn(ev))
  }

  // error simulates a dropped connection; with retry the browser keeps
  // reconnecting (CONNECTING), without it the source is closed for good.
  error(retry: boolean) {
    this.readyState = retry ? FakeEventSource.CONNECTING : FakeEventSource.CLOSED
    this.onerror?.(new Event('error'))
  }

  static install() {
    FakeEventSource.instances = []
    vi.stubGlobal('EventSource', FakeEventSource)
  }

  static latest(): FakeEventSource {
    const es = FakeEventSource.instances.at(-1)
    if (!es) throw new Error('no EventSource was opened')
    return es
  }
}

// setTimeZone sets the zone of local-time conversions (Node reads TZ on
// each use) and returns a function restoring the previous one. Specs have
// no Node types, hence the narrow cast.
export function setTimeZone(tz: string): () => void {
  const env = (globalThis as unknown as { process: { env: Record<string, string | undefined> } }).process.env
  const previous = env.TZ
  env.TZ = tz
  return () => {
    if (previous === undefined) delete env.TZ
    else env.TZ = previous
  }
}

export function progressJSON(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    campaign_id: CAMPAIGN_ID,
    campaign_status: 'sending',
    channels: [
      { channel: 'sms', queued: 100, processing: 10, sent: 140, failed: 2, retrying: 10, total: 262, sent_recently: 90, percent_done: 54.2 },
      { channel: 'email', queued: 120, processing: 6, sent: 110, failed: 4, retrying: 6, total: 246, sent_recently: 60, percent_done: 46.34 },
    ],
    totals: { queued: 220, processing: 16, sent: 250, failed: 6, retrying: 16, total: 508 },
    percent_done: 50.39,
    failures: [{ code: 'provider_rejected', count: 5 }, { code: 'consent_revoked', count: 1 }],
    throughput: { window_seconds: 10, sent: 150, per_second: 15 },
    generated_at: '2026-10-24T09:30:00Z',
    scheduled_at: null,
    ...overrides,
  }
}
