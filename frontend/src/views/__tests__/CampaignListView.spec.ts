import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it } from 'vitest'
import { createMemoryHistory } from 'vue-router'
import { makeRouter } from '../../router'
import { jsonResponse, setTimeZone, stubFetch } from '../../test/helpers'
import CampaignListView from '../CampaignListView.vue'

let restoreTZ = () => {}

function summary(id: string, status: string, scheduledAt: string | null = null) {
  return {
    id,
    brief: `Campaign ${id}`,
    status,
    counts: { total: 10, sms: 5, email: 4 },
    channels: ['sms'],
    approved_at: status === 'drafts_ready' ? null : '2026-10-22T12:05:00Z',
    scheduled_at: scheduledAt,
    created_at: '2026-10-22T12:00:00Z',
    updated_at: '2026-10-22T12:00:00Z',
  }
}

afterEach(() => restoreTZ())

describe('CampaignListView', () => {
  it('shows the scheduling states with their time and dashboard links', async () => {
    restoreTZ = setTimeZone('Europe/Istanbul')
    stubFetch({
      'GET /api/v1/campaigns': () =>
        jsonResponse({
          campaigns: [
            summary('a', 'scheduled', '2026-10-30T07:00:00Z'),
            summary('b', 'release_failed', '2026-10-29T07:00:00Z'),
            summary('c', 'expired', '2026-10-28T07:00:00Z'),
            summary('d', 'drafts_ready'),
          ],
        }),
    })
    const router = makeRouter(createMemoryHistory())
    await router.push('/campaigns')
    const wrapper = mount(CampaignListView, { global: { plugins: [router] } })
    await flushPromises()

    expect(wrapper.get('[data-status="scheduled"]').text()).toBe('Scheduled')
    expect(wrapper.get('[data-status="release_failed"]').text()).toBe('Release failed')
    expect(wrapper.get('[data-status="expired"]').text()).toBe('Expired')
    expect(wrapper.get('[data-testid="scheduled-a"]').text()).toMatch(/10:00.*\(UTC\+03:00\)/)
    for (const id of ['a', 'b', 'c']) expect(wrapper.find(`[data-testid="dashboard-${id}"]`).exists()).toBe(true)
    expect(wrapper.find('[data-testid="dashboard-d"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="scheduled-d"]').exists()).toBe(false)
  })
})
