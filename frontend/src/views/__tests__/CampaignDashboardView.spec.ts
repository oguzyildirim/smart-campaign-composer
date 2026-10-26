import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { createMemoryHistory } from 'vue-router'
import { makeRouter } from '../../router'
import { CAMPAIGN_ID, errorResponse, FakeEventSource, jsonResponse, progressJSON, setTimeZone, stubFetch } from '../../test/helpers'
import CampaignDashboardView from '../CampaignDashboardView.vue'

const progressPath = `GET /api/v1/campaigns/${CAMPAIGN_ID}/progress`

async function mountView() {
  const router = makeRouter(createMemoryHistory())
  await router.push(`/campaigns/${CAMPAIGN_ID}/dashboard`)
  const wrapper = mount(CampaignDashboardView, { props: { id: CAMPAIGN_ID }, global: { plugins: [router] } })
  await flushPromises()
  return wrapper
}

function cell(wrapper: Awaited<ReturnType<typeof mountView>>, channel: string, status: string): string {
  return wrapper.get(`[data-testid="count-${channel}-${status}"]`).text()
}

let restoreTZ = () => {}

beforeEach(() => {
  FakeEventSource.install()
})

afterEach(() => {
  restoreTZ()
  restoreTZ = () => {}
})

describe('CampaignDashboardView', () => {
  it('renders the snapshot with per-channel counts, failures, throughput, and an accessible progress bar', async () => {
    stubFetch({ [progressPath]: () => jsonResponse(progressJSON()) })
    const wrapper = await mountView()

    expect(cell(wrapper, 'sms', 'queued')).toBe('100')
    expect(cell(wrapper, 'sms', 'sent')).toBe('140')
    expect(cell(wrapper, 'email', 'failed')).toBe('4')
    expect(cell(wrapper, 'email', 'retrying')).toBe('6')
    expect(cell(wrapper, 'total', 'sent')).toBe('250')
    expect(wrapper.get('[data-testid="campaign-status"]').text()).toBe('Sending')
    expect(wrapper.get('[data-testid="throughput"]').text()).toContain('15')
    expect(wrapper.get('[data-testid="failures"]').text()).toContain('provider_rejected')
    expect(wrapper.get('[data-testid="failures"]').text()).toContain('5')

    const bar = wrapper.get('[data-testid="progress-total"]')
    expect(bar.attributes('role')).toBe('progressbar')
    expect(bar.attributes('aria-valuenow')).toBe('50')
    expect(bar.attributes('aria-valuemin')).toBe('0')
    expect(bar.attributes('aria-valuemax')).toBe('100')
    expect(bar.attributes('aria-label')).toBeTruthy()
    // Per-channel progress comes from the backend, not recomputed here.
    expect(wrapper.get('[data-testid="progress-sms"]').attributes('aria-valuenow')).toBe('54')
    expect(wrapper.get('[data-testid="progress-email"]').attributes('aria-valuenow')).toBe('46')

    expect(FakeEventSource.latest().url).toBe(`/api/v1/campaigns/${CAMPAIGN_ID}/events`)
  })

  it('replaces the state with each live snapshot instead of adding to it', async () => {
    stubFetch({ [progressPath]: () => jsonResponse(progressJSON()) })
    const wrapper = await mountView()
    const es = FakeEventSource.latest()
    es.open()
    await flushPromises()
    expect(wrapper.get('[data-testid="connection"]').text()).toContain('Live')

    const next = progressJSON({
      channels: [
        { channel: 'sms', queued: 50, processing: 5, sent: 200, failed: 2, retrying: 5, total: 262, sent_recently: 95, percent_done: 77.1 },
        { channel: 'email', queued: 120, processing: 6, sent: 110, failed: 4, retrying: 6, total: 246, sent_recently: 60, percent_done: 46.34 },
      ],
    })
    es.emit('snapshot', next, '2')
    es.emit('snapshot', next, '3') // the same snapshot twice must not double anything
    await flushPromises()

    expect(cell(wrapper, 'sms', 'sent')).toBe('200')
    expect(cell(wrapper, 'sms', 'queued')).toBe('50')
    expect(wrapper.findAll('[data-testid="channel-row"]')).toHaveLength(2)
  })

  it('shows reconnecting while the connection is down and the fresh snapshot afterwards', async () => {
    stubFetch({ [progressPath]: () => jsonResponse(progressJSON()) })
    const wrapper = await mountView()
    const es = FakeEventSource.latest()
    es.open()

    es.error(true)
    await flushPromises()
    expect(wrapper.get('[data-testid="connection"]').text()).toContain('Reconnecting')
    expect(cell(wrapper, 'sms', 'sent')).toBe('140') // last known numbers stay visible

    es.open()
    es.emit('snapshot', progressJSON({ totals: { queued: 0, processing: 0, sent: 300, failed: 6, retrying: 0, total: 306 } }), '7')
    await flushPromises()
    expect(wrapper.get('[data-testid="connection"]').text()).toContain('Live')
    expect(cell(wrapper, 'total', 'sent')).toBe('300')
  })

  it('closes the stream on complete and shows the final numbers', async () => {
    stubFetch({ [progressPath]: () => jsonResponse(progressJSON()) })
    const wrapper = await mountView()
    const es = FakeEventSource.latest()
    es.open()

    es.emit('complete', progressJSON({
      campaign_status: 'completed',
      channels: [
        { channel: 'sms', queued: 0, processing: 0, sent: 258, failed: 4, retrying: 0, total: 262, sent_recently: 0, percent_done: 100 },
        { channel: 'email', queued: 0, processing: 0, sent: 240, failed: 6, retrying: 0, total: 246, sent_recently: 0, percent_done: 100 },
      ],
      totals: { queued: 0, processing: 0, sent: 498, failed: 10, retrying: 0, total: 508 },
      percent_done: 100,
    }))
    await flushPromises()

    expect(es.closed).toBe(true)
    expect(wrapper.get('[data-testid="connection"]').text()).toContain('Completed')
    expect(wrapper.get('[data-testid="campaign-status"]').text()).toBe('Completed')
    expect(cell(wrapper, 'total', 'sent')).toBe('498')
    expect(wrapper.get('[data-testid="progress-total"]').attributes('aria-valuenow')).toBe('100')
  })

  it('does not open a stream for a campaign that is already completed', async () => {
    stubFetch({ [progressPath]: () => jsonResponse(progressJSON({ campaign_status: 'completed', percent_done: 100 })) })
    const wrapper = await mountView()

    expect(FakeEventSource.instances).toHaveLength(0)
    expect(wrapper.get('[data-testid="connection"]').text()).toContain('Completed')
  })

  it('closes the stream on unmount', async () => {
    stubFetch({ [progressPath]: () => jsonResponse(progressJSON()) })
    const wrapper = await mountView()
    const es = FakeEventSource.latest()

    wrapper.unmount()
    expect(es.closed).toBe(true)
  })

  it('shows the API error and opens no stream when the campaign cannot be loaded', async () => {
    stubFetch({ [progressPath]: () => errorResponse(404, 'not_found', 'campaign not found') })
    const wrapper = await mountView()

    expect(wrapper.get('[role="alert"]').text()).toContain('campaign not found')
    expect(FakeEventSource.instances).toHaveLength(0)
  })

  it('reports a stream the server refused and lets the user retry', async () => {
    stubFetch({ [progressPath]: () => jsonResponse(progressJSON()) })
    const wrapper = await mountView()
    FakeEventSource.latest().error(false)
    await flushPromises()

    expect(wrapper.get('[data-testid="connection"]').text()).toContain('Disconnected')
    await wrapper.get('[data-testid="reconnect"]').trigger('click')
    await flushPromises()
    expect(FakeEventSource.instances).toHaveLength(2)
    expect(FakeEventSource.instances[0]?.closed).toBe(true)
  })

  it('ignores malformed events and keeps the last valid state', async () => {
    stubFetch({ [progressPath]: () => jsonResponse(progressJSON()) })
    const wrapper = await mountView()
    const es = FakeEventSource.latest()
    es.open()

    es.emit('snapshot', '{not json')
    es.emit('snapshot', { campaign_status: 'sending', channels: 'nope' })
    await flushPromises()
    expect(cell(wrapper, 'sms', 'sent')).toBe('140')
  })

  it('shows an empty state for a campaign without deliveries yet', async () => {
    stubFetch({
      [progressPath]: () =>
        jsonResponse(progressJSON({
          campaign_status: 'drafts_ready',
          channels: [{ channel: 'sms', queued: 0, processing: 0, sent: 0, failed: 0, retrying: 0, total: 0, sent_recently: 0, percent_done: 0 }],
          totals: { queued: 0, processing: 0, sent: 0, failed: 0, retrying: 0, total: 0 },
          failures: [],
          percent_done: 0,
        })),
    })
    const wrapper = await mountView()
    expect(wrapper.get('[data-testid="empty"]').text()).toContain('No messages')
  })

  it('shows when a scheduled campaign will be released, then switches to progress on release', async () => {
    restoreTZ = setTimeZone('Europe/Istanbul')
    const none = { queued: 0, processing: 0, sent: 0, failed: 0, retrying: 0, total: 0 }
    stubFetch({
      [progressPath]: () =>
        jsonResponse(progressJSON({
          campaign_status: 'scheduled',
          scheduled_at: '2026-10-30T07:00:00Z',
          channels: [{ channel: 'sms', ...none, sent_recently: 0, percent_done: 0 }],
          totals: none,
          failures: [],
          percent_done: 0,
        })),
    })
    const wrapper = await mountView()
    expect(wrapper.get('[data-testid="campaign-status"]').text()).toBe('Scheduled')
    expect(wrapper.get('[data-testid="scheduled-banner"]').text()).toMatch(/10:00.*\(UTC\+03:00\)/)
    expect(wrapper.get('[data-testid="empty"]').text()).toContain('released')
    const es = FakeEventSource.latest()
    es.open()

    // The release arrives as an ordinary snapshot: no reload needed.
    es.emit('snapshot', progressJSON({ campaign_status: 'sending', scheduled_at: '2026-10-30T07:00:00Z' }), '2')
    await flushPromises()
    expect(wrapper.get('[data-testid="campaign-status"]').text()).toBe('Sending')
    expect(wrapper.find('[data-testid="empty"]').exists()).toBe(false)
    expect(cell(wrapper, 'sms', 'sent')).toBe('140')
    expect(wrapper.get('[data-testid="scheduled-banner"]').text()).toContain('Scheduled for')
  })

  it.each(['release_failed', 'expired'] as const)('explains a %s campaign that sent nothing', async (status) => {
    const none = { queued: 0, processing: 0, sent: 0, failed: 0, retrying: 0, total: 0 }
    stubFetch({
      [progressPath]: () =>
        jsonResponse(progressJSON({
          campaign_status: status,
          scheduled_at: '2026-10-30T07:00:00Z',
          channels: [],
          totals: none,
          failures: [],
          percent_done: 0,
        })),
    })
    const wrapper = await mountView()
    expect(wrapper.get('[data-testid="empty"]').text()).toContain('Nothing was sent')
    // Expired is final: no stream. A failed release may still be canceled.
    expect(FakeEventSource.instances).toHaveLength(status === 'expired' ? 0 : 1)
    if (status === 'expired') expect(wrapper.get('[data-testid="connection"]').text()).toContain('Ended')
  })

  it('closes the stream when the campaign expires', async () => {
    stubFetch({ [progressPath]: () => jsonResponse(progressJSON({ campaign_status: 'scheduled', scheduled_at: '2026-10-30T07:00:00Z' })) })
    const wrapper = await mountView()
    const es = FakeEventSource.latest()
    es.open()
    es.emit('complete', progressJSON({ campaign_status: 'expired', scheduled_at: '2026-10-30T07:00:00Z' }))
    await flushPromises()
    expect(es.closed).toBe(true)
    expect(wrapper.get('[data-testid="connection"]').text()).toContain('Ended')
  })
})
