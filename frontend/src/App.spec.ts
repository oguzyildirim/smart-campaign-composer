import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import App from './App.vue'

describe('App', () => {
  it('renders the application shell', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('{"status":"ok"}', { status: 200 })))
    const wrapper = mount(App)
    await flushPromises()

    expect(wrapper.get('h1').text()).toBe('Smart Campaign Composer')
    expect(wrapper.find('[role="status"]').exists()).toBe(true)
  })
})
