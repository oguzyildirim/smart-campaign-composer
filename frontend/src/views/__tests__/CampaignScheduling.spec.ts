import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { createMemoryHistory } from 'vue-router'
import { makeRouter } from '../../router'
import { CAMPAIGN_ID, campaignJSON, errorResponse, jsonResponse, previewJSON, setTimeZone, stubFetch } from '../../test/helpers'
import CampaignReviewView from '../CampaignReviewView.vue'

const path = `/api/v1/campaigns/${CAMPAIGN_ID}`
let restoreTZ = () => {}

const approval = { approved_by: 'Deniz', approved_at: '2026-10-22T12:05:00Z' }
const scheduledJSON = (overrides: Record<string, unknown> = {}) =>
  campaignJSON({ status: 'scheduled', version: 3, scheduled_at: '2026-10-30T07:00:00Z', ...approval, ...overrides })

async function mountView() {
  const router = makeRouter(createMemoryHistory())
  await router.push(`/campaigns/${CAMPAIGN_ID}`)
  const wrapper = mount(CampaignReviewView, { props: { id: CAMPAIGN_ID }, global: { plugins: [router] } })
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  restoreTZ = setTimeZone('Europe/Istanbul')
})

afterEach(() => restoreTZ())

describe('CampaignReviewView scheduling', () => {
  it('approves for a local date and time, sent with its offset', async () => {
    const { requests } = stubFetch({
      [`GET ${path}`]: () => jsonResponse(campaignJSON()),
      [`GET ${path}/preview`]: () => jsonResponse(previewJSON()),
      [`POST ${path}/approve`]: () => jsonResponse(scheduledJSON()),
    })
    const wrapper = await mountView()
    await wrapper.get('[data-testid="approve"]').trigger('click')
    const dialog = wrapper.get('[role="dialog"]')
    await wrapper.get('#approver-name').setValue('Deniz')
    await wrapper.get('[data-testid="when-schedule"]').setValue(true)
    expect(dialog.text()).toContain('estimate')
    // Scheduling without a time is not submitted.
    expect(wrapper.get('[data-testid="confirm-approve"]').attributes('disabled')).toBeDefined()
    await wrapper.get('#scheduled-at').setValue('2026-10-30T10:00')
    expect(wrapper.get('[data-testid="confirm-approve"]').text()).toBe('Schedule')
    await wrapper.get('[role="dialog"] form').trigger('submit')
    await flushPromises()

    const approvals = requests.filter((r) => r.method === 'POST' && r.url.endsWith('/approve'))
    expect(approvals).toHaveLength(1)
    expect(approvals[0]?.body).toEqual({
      version: 2,
      content_hash: 'a'.repeat(64),
      approved_by: 'Deniz',
      scheduled_at: '2026-10-30T10:00:00+03:00',
    })
    expect(wrapper.vm.$router.currentRoute.value.fullPath).toBe(`/campaigns/${CAMPAIGN_ID}/dashboard`)
  })

  it('sends now without a scheduled time by default', async () => {
    const { requests } = stubFetch({
      [`GET ${path}`]: () => jsonResponse(campaignJSON()),
      [`GET ${path}/preview`]: () => jsonResponse(previewJSON()),
      [`POST ${path}/approve`]: () => jsonResponse(campaignJSON({ status: 'approved', version: 3, ...approval })),
    })
    const wrapper = await mountView()
    await wrapper.get('[data-testid="approve"]').trigger('click')
    expect(wrapper.get('[role="dialog"]').text()).not.toContain('estimate')
    await wrapper.get('#approver-name').setValue('Deniz')
    await wrapper.get('[role="dialog"] form').trigger('submit')
    await flushPromises()
    const body = requests.find((r) => r.url.endsWith('/approve'))?.body as Record<string, unknown>
    expect(body).not.toHaveProperty('scheduled_at')
  })

  it('shows a backend rejection of the scheduled time', async () => {
    stubFetch({
      [`GET ${path}`]: () => jsonResponse(campaignJSON()),
      [`GET ${path}/preview`]: () => jsonResponse(previewJSON()),
      [`POST ${path}/approve`]: () =>
        errorResponse(400, 'invalid_request', 'the request is invalid', {
          details: [{ path: 'scheduled_at', message: 'must be at least 1m0s from now' }],
        }),
    })
    const wrapper = await mountView()
    await wrapper.get('[data-testid="approve"]').trigger('click')
    await wrapper.get('#approver-name').setValue('Deniz')
    await wrapper.get('[data-testid="when-schedule"]').setValue(true)
    await wrapper.get('#scheduled-at').setValue('2026-10-22T15:00')
    await wrapper.get('[role="dialog"] form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[role="dialog"] [role="alert"]').text()).toContain('must be at least 1m0s from now')
  })

  it('shows a scheduled campaign locked, with its time and an estimate label, and reschedules it', async () => {
    const { requests } = stubFetch({
      [`GET ${path}`]: () => jsonResponse(scheduledJSON()),
      [`GET ${path}/preview`]: () => jsonResponse(previewJSON({ version: 3 })),
      [`POST ${path}/reschedule`]: () => jsonResponse(scheduledJSON({ version: 4, scheduled_at: '2026-11-02T06:30:00Z' })),
    })
    const wrapper = await mountView()
    expect(wrapper.get('[data-status="scheduled"]').text()).toBe('Scheduled')
    const panel = wrapper.get('[data-testid="schedule-panel"]')
    expect(panel.text()).toMatch(/10:00.*\(UTC\+03:00\)/)
    expect(panel.text()).toContain('Approved by Deniz')
    expect(wrapper.get('[data-testid="counts-estimate"]').text()).toContain('estimate')
    expect(wrapper.get('#field-sms_body').attributes('readonly')).toBeDefined()
    expect(wrapper.find('[data-testid="approve"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="approved-banner"]').exists()).toBe(false)

    await wrapper.get('#schedule-actor').setValue('Ece')
    await wrapper.get('#reschedule-at').setValue('2026-11-02T09:30')
    await wrapper.get('[data-testid="reschedule"]').trigger('click')
    await wrapper.get('[data-testid="reschedule"]').trigger('click')
    await flushPromises()

    const calls = requests.filter((r) => r.url.endsWith('/reschedule'))
    expect(calls).toHaveLength(1)
    expect(calls[0]?.body).toEqual({
      version: 3,
      content_hash: 'a'.repeat(64),
      scheduled_at: '2026-11-02T09:30:00+03:00',
      actor: 'Ece',
    })
    expect(wrapper.get('[data-testid="schedule-panel"]').text()).toMatch(/9:30.*\(UTC\+03:00\)/)
  })

  it('cancels a scheduled campaign back to editable drafts', async () => {
    const { requests } = stubFetch({
      [`GET ${path}`]: () => jsonResponse(scheduledJSON()),
      [`GET ${path}/preview`]: () => jsonResponse(previewJSON({ version: requests.some((r) => r.url.endsWith('/cancel')) ? 4 : 3 })),
      [`POST ${path}/cancel`]: () => jsonResponse(campaignJSON({ version: 4 })),
    })
    const wrapper = await mountView()
    expect(wrapper.get('[data-testid="cancel-schedule"]').attributes('disabled')).toBeDefined() // no name yet
    await wrapper.get('#schedule-actor').setValue('Ece')
    await wrapper.get('[data-testid="cancel-schedule"]').trigger('click')
    await flushPromises()

    expect(requests.find((r) => r.url.endsWith('/cancel'))?.body).toEqual({
      version: 3,
      content_hash: 'a'.repeat(64),
      actor: 'Ece',
    })
    expect(wrapper.find('[data-testid="schedule-panel"]').exists()).toBe(false)
    expect(wrapper.find('[data-status="drafts_ready"]').exists()).toBe(true)
    expect(wrapper.get('#field-sms_body').attributes('readonly')).toBeUndefined()
    expect(wrapper.find('[data-testid="approve"]').exists()).toBe(true)
  })

  it('reports a cancel that lost the race with the release', async () => {
    stubFetch({
      [`GET ${path}`]: () => jsonResponse(scheduledJSON()),
      [`GET ${path}/preview`]: () => jsonResponse(previewJSON({ version: 3 })),
      [`POST ${path}/cancel`]: () =>
        errorResponse(409, 'invalid_state', 'only scheduled, release_failed, or expired campaigns can be canceled'),
    })
    const wrapper = await mountView()
    await wrapper.get('#schedule-actor').setValue('Ece')
    await wrapper.get('[data-testid="cancel-schedule"]').trigger('click')
    await flushPromises()
    const alert = wrapper.get('[data-testid="schedule-error"]').text()
    expect(alert).toContain('can be canceled')
    expect(alert).toContain('Reload')
    expect(wrapper.find('[data-testid="schedule-panel"]').exists()).toBe(true)
  })

  it('shows why a release failed and offers only cancel', async () => {
    stubFetch({
      [`GET ${path}`]: () =>
        jsonResponse(
          scheduledJSON({
            status: 'release_failed',
            release_failure: {
              code: 'invalid_content',
              error: 'the content exceeds a limit for recipients in this audience',
              details: [{ path: 'sms.body', message: 'is 171 characters for the longest first_name; the limit is 160' }],
            },
          }),
        ),
      [`GET ${path}/preview`]: () => jsonResponse(previewJSON({ version: 3 })),
    })
    const wrapper = await mountView()
    expect(wrapper.get('[data-status="release_failed"]').text()).toBe('Release failed')
    const panel = wrapper.get('[data-testid="schedule-panel"]')
    expect(panel.text()).toContain('exceeds a limit')
    expect(panel.text()).toContain('is 171 characters')
    expect(panel.text()).toContain('Nothing was sent')
    expect(wrapper.find('[data-testid="reschedule"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="cancel-schedule"]').exists()).toBe(true)
  })

  it('explains an expired campaign and offers only cancel', async () => {
    stubFetch({
      [`GET ${path}`]: () => jsonResponse(scheduledJSON({ status: 'expired' })),
      [`GET ${path}/preview`]: () => jsonResponse(previewJSON({ version: 3 })),
    })
    const wrapper = await mountView()
    expect(wrapper.get('[data-status="expired"]').text()).toBe('Expired')
    expect(wrapper.get('[data-testid="schedule-panel"]').text()).toContain('was not released in time')
    expect(wrapper.find('[data-testid="reschedule"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="cancel-schedule"]').exists()).toBe(true)
  })
})
