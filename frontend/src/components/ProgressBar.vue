<script setup lang="ts">
import { computed } from 'vue'

// An accessible progress bar: the value is exposed to assistive
// technology and printed as text, never conveyed by the bar alone.
const props = defineProps<{ value: number; label: string }>()
// Attributes (e.g. data-testid) go to the progressbar element itself.
defineOptions({ inheritAttrs: false })

const rounded = computed(() => Math.round(Math.min(100, Math.max(0, props.value))))
</script>

<template>
  <div class="progress">
    <div
      class="progress__track"
      role="progressbar"
      :aria-label="label"
      aria-valuemin="0"
      aria-valuemax="100"
      :aria-valuenow="rounded"
      :aria-valuetext="`${rounded}% done`"
      v-bind="$attrs"
    >
      <div
        class="progress__fill"
        :style="{ width: `${rounded}%` }"
      />
    </div>
    <span class="progress__text">{{ rounded }}%</span>
  </div>
</template>

<style scoped>
.progress {
  display: flex;
  align-items: center;
  gap: 0.75rem;
}
.progress__track {
  flex: 1;
  height: 0.75rem;
  border-radius: 999px;
  background: #e2e8f0;
  overflow: hidden;
}
.progress__fill {
  height: 100%;
  background: #1d4ed8;
  transition: width 0.3s ease;
}
.progress__text {
  min-width: 3.5rem;
  text-align: right;
  font-variant-numeric: tabular-nums;
}
</style>
