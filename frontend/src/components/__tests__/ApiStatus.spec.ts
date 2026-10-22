import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import ApiStatus from '../ApiStatus.vue'

function stubFetch(impl: () => Promise<Response>) {
  const fetchMock = vi.fn(impl)
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

describe('ApiStatus', () => {
  it('shows a loading state, then online when the API is healthy', async () => {
    const fetchMock = stubFetch(async () => jsonResponse({ status: 'ok' }))
    const wrapper = mount(ApiStatus)

    expect(wrapper.attributes('data-state')).toBe('loading')
    await flushPromises()

    expect(fetchMock).toHaveBeenCalledWith('/api/healthz', expect.anything())
    expect(wrapper.attributes('data-state')).toBe('online')
    expect(wrapper.text()).toBe('API online')
    expect(wrapper.attributes('role')).toBe('status')
  })

  it('shows offline when the API returns an error status', async () => {
    stubFetch(async () => jsonResponse({ error: 'boom' }, 503))
    const wrapper = mount(ApiStatus)
    await flushPromises()

    expect(wrapper.attributes('data-state')).toBe('offline')
  })

  it('shows offline when the network request fails', async () => {
    stubFetch(async () => Promise.reject(new TypeError('network down')))
    const wrapper = mount(ApiStatus)
    await flushPromises()

    expect(wrapper.attributes('data-state')).toBe('offline')
  })

  it('treats an unexpected response shape as offline', async () => {
    stubFetch(async () => jsonResponse({ status: '<img src=x onerror=alert(1)>' }))
    const wrapper = mount(ApiStatus)
    await flushPromises()

    expect(wrapper.attributes('data-state')).toBe('offline')
    expect(wrapper.html()).not.toContain('<img')
  })
})
