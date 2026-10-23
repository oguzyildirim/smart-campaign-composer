import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { createMemoryHistory } from 'vue-router'
import { makeRouter } from '../../router'
import { CAMPAIGN_ID, campaignJSON, errorResponse, jsonResponse, stubFetch } from '../../test/helpers'
import NewCampaignView from '../NewCampaignView.vue'

async function mountView() {
  const router = makeRouter(createMemoryHistory())
  await router.push('/campaigns/new')
  const wrapper = mount(NewCampaignView, { global: { plugins: [router] } })
  return { wrapper, router }
}

describe('NewCampaignView', () => {
  it('counts characters and disables submit for empty or overlong briefs', async () => {
    stubFetch({})
    const { wrapper } = await mountView()
    const submit = wrapper.get('button[type="submit"]')
    expect(submit.attributes('disabled')).toBeDefined()

    await wrapper.get('textarea').setValue('İstanbul ş')
    expect(wrapper.get('[data-testid="brief-count"]').text()).toBe('10/1000 characters')
    expect(submit.attributes('disabled')).toBeUndefined()

    await wrapper.get('textarea').setValue('   ')
    expect(submit.attributes('disabled')).toBeDefined()

    await wrapper.get('textarea').setValue('a'.repeat(1001))
    expect(wrapper.get('textarea').attributes('aria-invalid')).toBe('true')
    expect(submit.attributes('disabled')).toBeDefined()
  })

  it('shows a pending state, then navigates to the new campaign', async () => {
    let resolve!: (r: Response) => void
    const { requests } = stubFetch({
      'POST /api/v1/campaigns': () => new Promise<Response>((r) => (resolve = r)),
      [`GET /api/v1/campaigns/${CAMPAIGN_ID}`]: () => jsonResponse(campaignJSON()),
      [`GET /api/v1/campaigns/${CAMPAIGN_ID}/preview`]: () => jsonResponse({}),
    })
    const { wrapper, router } = await mountView()
    await wrapper.get('textarea').setValue('İstanbul sepet')
    await wrapper.get('form').trigger('submit')
    await wrapper.get('form').trigger('submit')

    expect(wrapper.get('button[type="submit"]').text()).toBe('Finding the audience…')
    expect(wrapper.get('textarea').attributes('disabled')).toBeDefined()
    expect(requests.filter((r) => r.method === 'POST')).toHaveLength(1)
    expect(requests[0]?.body).toEqual({ brief: 'İstanbul sepet' })

    resolve(jsonResponse(campaignJSON({ status: 'audience_ready', drafts: null, validation: null }), 201))
    await flushPromises()
    expect(router.currentRoute.value.path).toBe(`/campaigns/${CAMPAIGN_ID}`)
  })

  it('renders API errors and unsupported criteria as text', async () => {
    stubFetch({
      'POST /api/v1/campaigns': () =>
        errorResponse(422, 'unsupported_brief', 'the brief only uses criteria that audience rules cannot express', {
          unsupported_criteria: ['age', '<img src=x onerror=alert(1)>'],
        }),
    })
    const { wrapper } = await mountView()
    await wrapper.get('textarea').setValue('users older than 30')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain('audience rules cannot express')
    const items = wrapper.findAll('[data-testid="unsupported"] li').map((li) => li.text())
    expect(items).toEqual(['age', '<img src=x onerror=alert(1)>'])
    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeUndefined()
  })

  it('shows a network error', async () => {
    stubFetch({ 'POST /api/v1/campaigns': () => Promise.reject(new TypeError('failed')) })
    const { wrapper } = await mountView()
    await wrapper.get('textarea').setValue('İstanbul')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('The API could not be reached.')
  })
})
