import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory } from 'vue-router'
import { makeRouter } from '../../router'
import {
  CAMPAIGN_ID,
  campaignJSON,
  controlledStream,
  drafts,
  errorResponse,
  jsonResponse,
  previewJSON,
  stubFetch,
} from '../../test/helpers'
import CampaignReviewView from '../CampaignReviewView.vue'

const path = `/api/v1/campaigns/${CAMPAIGN_ID}`

async function mountView() {
  const router = makeRouter(createMemoryHistory())
  await router.push(`/campaigns/${CAMPAIGN_ID}`)
  const wrapper = mount(CampaignReviewView, { props: { id: CAMPAIGN_ID }, global: { plugins: [router] } })
  await flushPromises()
  return wrapper
}

afterEach(() => {
  vi.useRealTimers()
})

describe('CampaignReviewView', () => {
  it('shows the audience and streams drafts as they arrive', async () => {
    const stream = controlledStream()
    let current = campaignJSON({ status: 'audience_ready', drafts: null, validation: null, version: 1 })
    const { requests } = stubFetch({
      [`GET ${path}`]: () => jsonResponse(current),
      [`POST ${path}/drafts`]: () => stream.response,
      [`GET ${path}/preview`]: () => jsonResponse(previewJSON()),
    })
    const wrapper = await mountView()

    expect(wrapper.get('[data-testid="count-sms"]').text()).toBe('262')
    expect(wrapper.text()).toContain('City is İstanbul')
    await wrapper.get('[data-testid="generate"]').trigger('click')
    await flushPromises()

    stream.push(': generating\n\n')
    stream.event('delta', { attempt: 1, field: 'sms_body', text: 'Merhaba {{first' })
    await flushPromises()
    expect(wrapper.get('[data-testid="stream-sms_body"]').text()).toBe('Merhaba {{first')

    stream.event('delta', { attempt: 1, field: 'sms_body', text: '_name}}, sepet' })
    stream.event('delta', { attempt: 1, field: 'email_subject', text: 'Konu <b>x</b>' })
    await flushPromises()
    expect(wrapper.get('[data-testid="stream-sms_body"]').text()).toBe('Merhaba {{first_name}}, sepet')
    expect(wrapper.get('[data-testid="stream-email_subject"]').text()).toBe('Konu <b>x</b>')
    expect(wrapper.find('b').exists()).toBe(false)

    // A repair attempt replaces the text instead of appending to it.
    stream.event('delta', { attempt: 2, field: 'sms_body', text: 'Selam' })
    await flushPromises()
    expect(wrapper.get('[data-testid="stream-sms_body"]').text()).toBe('Selam')

    current = campaignJSON()
    stream.event('done', {
      status: 'drafts_ready',
      drafts,
      validation: current.validation,
      version: 2,
      content_hash: 'a'.repeat(64),
    })
    stream.close()
    await flushPromises()

    expect(wrapper.find('[data-testid="streaming"]').exists()).toBe(false)
    expect((wrapper.get('#field-sms_body').element as HTMLTextAreaElement).value).toBe(drafts.sms_body)
    expect(wrapper.find('[data-testid="preview-valid"]').exists()).toBe(true)
    expect(requests.filter((r) => r.method === 'POST')).toHaveLength(1)
  })

  it('shows a stream error event and keeps the generate button', async () => {
    const stream = controlledStream()
    stubFetch({
      [`GET ${path}`]: () => jsonResponse(campaignJSON({ status: 'audience_ready', drafts: null, validation: null })),
      [`POST ${path}/drafts`]: () => stream.response,
    })
    const wrapper = await mountView()
    await wrapper.get('[data-testid="generate"]').trigger('click')
    await flushPromises()
    stream.event('error', { code: 'invalid_output', message: 'the AI response could not be validated' })
    stream.close()
    await flushPromises()

    expect(wrapper.get('[data-testid="stream-error"]').text()).toContain('could not be validated')
    expect(wrapper.get('[data-testid="stream-error"]').text()).toContain('Try again')
    expect(wrapper.find('[data-testid="generate"]').exists()).toBe(true)
  })

  it('shows a 409 when a generation is already running', async () => {
    stubFetch({
      [`GET ${path}`]: () => jsonResponse(campaignJSON({ status: 'audience_ready', drafts: null, validation: null })),
      [`POST ${path}/drafts`]: () =>
        errorResponse(409, 'generation_in_progress', 'drafts are being generated for this campaign'),
    })
    const wrapper = await mountView()
    await wrapper.get('[data-testid="generate"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="stream-error"]').text()).toContain('Wait for the current generation')
  })

  it('aborts the draft stream on unmount', async () => {
    const stream = controlledStream()
    const { requests } = stubFetch({
      [`GET ${path}`]: () => jsonResponse(campaignJSON({ status: 'audience_ready', drafts: null, validation: null })),
      [`POST ${path}/drafts`]: () => stream.response,
    })
    const wrapper = await mountView()
    await wrapper.get('[data-testid="generate"]').trigger('click')
    await flushPromises()
    const signal = requests.find((r) => r.method === 'POST')?.signal
    expect(signal?.aborted).toBe(false)
    wrapper.unmount()
    expect(signal?.aborted).toBe(true)
  })

  it('autosaves edits, refreshes the preview, and blocks approval while the preview is invalid', async () => {
    vi.useFakeTimers()
    const { requests } = stubFetch({
      [`GET ${path}`]: () => jsonResponse(campaignJSON()),
      [`GET ${path}/preview`]: () => {
        const edited = requests.some((r) => r.method === 'PATCH')
        return jsonResponse(
          edited
            ? previewJSON({
                version: 3,
                validation: {
                  valid: false,
                  errors: [{ path: 'sms.body', message: 'unknown variable; allowed: {{first_name}}, {{city}}' }],
                  worst_case_lengths: { sms_body: 12, email_subject: 50, email_body: 70 },
                  limits: { sms_body: 160, email_subject: 80, email_body: 2000 },
                },
              })
            : previewJSON(),
        )
      },
      [`PATCH ${path}`]: (req) =>
        jsonResponse(campaignJSON({ version: 3, drafts: { ...drafts, sms_body: (req.body as { sms_body: string }).sms_body } })),
    })
    const wrapper = await mountView()
    expect(wrapper.get('[data-testid="approve"]').attributes('disabled')).toBeUndefined()

    await wrapper.get('#field-sms_body').setValue('Hi {{phone}}')
    expect(wrapper.get('[data-testid="approve"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="save-state"]').text()).toBe('Unsaved changes')
    expect(wrapper.get('[data-testid="count-sms_body"]').text()).toContain('12 characters as written')
    expect(wrapper.get('[data-testid="count-sms_body"]').text()).toContain('limit 160')

    await vi.advanceTimersByTimeAsync(700)
    await flushPromises()
    const patch = requests.find((r) => r.method === 'PATCH')
    expect(patch?.body).toEqual({ version: 2, channels: ['sms', 'email'], ...drafts, sms_body: 'Hi {{phone}}' })
    expect(wrapper.get('[data-testid="preview-invalid"]').text()).toContain('unknown variable')
    expect(wrapper.get('[role="alert"]').text()).toContain('unknown variable')
    expect(wrapper.get('[data-testid="approve"]').attributes('disabled')).toBeDefined()
  })

  it('sends one approval on double click, then becomes read-only', async () => {
    let resolveApprove!: (r: Response) => void
    const { requests } = stubFetch({
      [`GET ${path}`]: () => jsonResponse(campaignJSON()),
      [`GET ${path}/preview`]: () => jsonResponse(previewJSON()),
      [`POST ${path}/approve`]: () => new Promise<Response>((r) => (resolveApprove = r)),
    })
    const wrapper = await mountView()
    await wrapper.get('[data-testid="approve"]').trigger('click')
    expect(wrapper.get('[role="dialog"]').text()).toContain('SMS (262) and email (246)')

    const confirm = wrapper.get('[data-testid="confirm-approve"]')
    expect(confirm.attributes('disabled')).toBeDefined() // no name yet
    await wrapper.get('#approver-name').setValue('Deniz Operator')
    await wrapper.get('[role="dialog"] form').trigger('submit')
    await wrapper.get('[role="dialog"] form').trigger('submit')
    await confirm.trigger('click')

    expect(confirm.attributes('disabled')).toBeDefined()
    expect(confirm.text()).toBe('Approving…')
    const approvals = requests.filter((r) => r.method === 'POST' && r.url.endsWith('/approve'))
    expect(approvals).toHaveLength(1)
    expect(approvals[0]?.body).toEqual({ version: 2, content_hash: 'a'.repeat(64), approved_by: 'Deniz Operator' })

    resolveApprove(
      jsonResponse(campaignJSON({ status: 'approved', version: 3, approved_by: 'Deniz Operator', approved_at: '2026-10-22T12:05:00Z' })),
    )
    await flushPromises()

    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="approved-banner"]').text()).toContain('Approved by Deniz Operator')
    expect(wrapper.find('[data-testid="approve"]').exists()).toBe(false)
    expect(wrapper.get('#field-sms_body').attributes('readonly')).toBeDefined()
    expect(wrapper.get('fieldset').attributes('disabled')).toBeDefined()
    // A fresh approval goes on to the live dashboard.
    expect(wrapper.vm.$router.currentRoute.value.fullPath).toBe(`/campaigns/${CAMPAIGN_ID}/dashboard`)
  })

  it('shows approval errors from the API and allows a retry', async () => {
    stubFetch({
      [`GET ${path}`]: () => jsonResponse(campaignJSON()),
      [`GET ${path}/preview`]: () => jsonResponse(previewJSON()),
      [`POST ${path}/approve`]: () =>
        errorResponse(409, 'version_conflict', 'the campaign was changed by someone else; reload it'),
    })
    const wrapper = await mountView()
    await wrapper.get('[data-testid="approve"]').trigger('click')
    await wrapper.get('#approver-name').setValue('Deniz')
    await wrapper.get('[role="dialog"] form').trigger('submit')
    await flushPromises()

    expect(wrapper.get('[role="dialog"] [role="alert"]').text()).toContain('Reload the page')
    expect(wrapper.get('[data-testid="confirm-approve"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.find('[data-testid="approved-banner"]').exists()).toBe(false)
  })

  it('renders approved campaigns read-only on load', async () => {
    stubFetch({
      [`GET ${path}`]: () =>
        jsonResponse(campaignJSON({ status: 'approved', approved_by: 'Deniz', approved_at: '2026-10-22T12:05:00Z' })),
      [`GET ${path}/preview`]: () => jsonResponse(previewJSON()),
    })
    const wrapper = await mountView()
    expect(wrapper.find('[data-testid="approved-banner"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="approve"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="generate"]').exists()).toBe(false)
    expect(wrapper.get('#field-email_subject').attributes('readonly')).toBeDefined()
    // Reopening an approved campaign stays on the review page, with a link.
    expect(wrapper.vm.$router.currentRoute.value.fullPath).toBe(`/campaigns/${CAMPAIGN_ID}`)
    expect(wrapper.get('[data-testid="dashboard-link"]').attributes('href')).toBe(`/campaigns/${CAMPAIGN_ID}/dashboard`)
  })

  it('shows no dashboard link before approval', async () => {
    stubFetch({
      [`GET ${path}`]: () => jsonResponse(campaignJSON()),
      [`GET ${path}/preview`]: () => jsonResponse(previewJSON()),
    })
    const wrapper = await mountView()
    expect(wrapper.find('[data-testid="dashboard-link"]').exists()).toBe(false)
  })

  it.each(['sending', 'completed'] as const)('renders %s campaigns read-only with their status', async (status) => {
    stubFetch({
      [`GET ${path}`]: () =>
        jsonResponse(campaignJSON({ status, approved_by: 'Deniz', approved_at: '2026-10-22T12:05:00Z' })),
      [`GET ${path}/preview`]: () => jsonResponse(previewJSON()),
    })
    const wrapper = await mountView()
    expect(wrapper.get(`[data-status="${status}"]`).text()).toBe(status === 'sending' ? 'Sending' : 'Completed')
    expect(wrapper.find('[data-testid="approved-banner"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="approve"]').exists()).toBe(false)
    expect(wrapper.get('#field-sms_body').attributes('readonly')).toBeDefined()
  })

  it('shows load errors', async () => {
    stubFetch({ [`GET ${path}`]: () => errorResponse(404, 'not_found', 'campaign not found') })
    const wrapper = await mountView()
    expect(wrapper.get('[role="alert"]').text()).toBe('campaign not found')
  })

  it('treats a malformed response as an error', async () => {
    stubFetch({ [`GET ${path}`]: () => jsonResponse(campaignJSON({ status: 'sent' })) })
    const wrapper = await mountView()
    expect(wrapper.get('[role="alert"]').text()).toContain('unexpected response')
  })
})
