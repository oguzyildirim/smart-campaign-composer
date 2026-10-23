import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { createMemoryHistory } from 'vue-router'
import App from './App.vue'
import { makeRouter } from './router'
import { jsonResponse, stubFetch } from './test/helpers'

describe('App', () => {
  it('renders the shell and the campaign list at /', async () => {
    stubFetch({
      'GET /api/healthz': () => jsonResponse({ status: 'ok' }),
      'GET /api/v1/campaigns': () => jsonResponse({ campaigns: [] }),
    })
    const router = makeRouter(createMemoryHistory())
    await router.push('/')
    const wrapper = mount(App, { global: { plugins: [router] } })
    await flushPromises()

    expect(router.currentRoute.value.path).toBe('/campaigns')
    expect(wrapper.get('h1').text()).toBe('Smart Campaign Composer')
    expect(wrapper.text()).toContain('No campaigns yet.')
  })
})
