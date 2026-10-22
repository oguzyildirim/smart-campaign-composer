<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { fetchHealth } from '../api/health'

type State = 'loading' | 'online' | 'offline'

const state = ref<State>('loading')
const controller = new AbortController()

const labels: Record<State, string> = {
  loading: 'Checking API…',
  online: 'API online',
  offline: 'API unreachable',
}

onMounted(async () => {
  try {
    const health = await fetchHealth(controller.signal)
    state.value = health.status === 'ok' ? 'online' : 'offline'
  } catch {
    if (!controller.signal.aborted) state.value = 'offline'
  }
})

onUnmounted(() => controller.abort())
</script>

<template>
  <p
    class="api-status"
    :class="`api-status--${state}`"
    role="status"
    aria-live="polite"
    :data-state="state"
  >
    {{ labels[state] }}
  </p>
</template>

<style scoped>
.api-status {
  display: inline-block;
  margin: 0;
  padding: 0.25rem 0.75rem;
  border-radius: 999px;
  font-size: 0.875rem;
  background: #eee;
  color: #333;
}
.api-status--online {
  background: #dcfce7;
  color: #14532d;
}
.api-status--offline {
  background: #fee2e2;
  color: #7f1d1d;
}
</style>
