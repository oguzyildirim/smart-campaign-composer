<script setup lang="ts">
import { computed, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ApiError } from '../api/client'
import { createCampaign, MAX_BRIEF_CHARS } from '../api/campaigns'
import { describeError, isAbort } from '../composables/errors'

const router = useRouter()
const brief = ref('')
const pending = ref(false)
const error = ref('')
const unsupported = ref<string[]>([])
const controller = new AbortController()

const length = computed(() => Array.from(brief.value).length)
const tooLong = computed(() => length.value > MAX_BRIEF_CHARS)
const canSubmit = computed(() => !pending.value && brief.value.trim() !== '' && !tooLong.value)

async function submit() {
  if (!canSubmit.value) return
  pending.value = true
  error.value = ''
  unsupported.value = []
  try {
    const c = await createCampaign(brief.value, controller.signal)
    await router.push(`/campaigns/${c.id}`)
  } catch (err) {
    if (isAbort(err)) return
    error.value = describeError(err)
    if (err instanceof ApiError) unsupported.value = err.unsupportedCriteria
  } finally {
    pending.value = false
  }
}

onUnmounted(() => controller.abort())
</script>

<template>
  <section class="panel">
    <h2>New campaign</h2>
    <form @submit.prevent="submit">
      <label for="brief">Describe the campaign: who should get it and what it should say</label>
      <textarea
        id="brief"
        v-model="brief"
        rows="5"
        :disabled="pending"
        aria-describedby="brief-count"
        :aria-invalid="tooLong"
        placeholder="Son 30 günde sepetine ürün ekleyip alışveriş yapmayan İstanbul'daki kullanıcılara %30 indirim hatırlatması gönder."
      />
      <p
        id="brief-count"
        class="muted"
        :class="{ over: tooLong }"
        data-testid="brief-count"
      >
        {{ length }}/{{ MAX_BRIEF_CHARS }} characters
      </p>
      <p
        v-if="error"
        class="error"
        role="alert"
      >
        {{ error }}
      </p>
      <ul
        v-if="unsupported.length > 0"
        class="warning"
        data-testid="unsupported"
      >
        <li
          v-for="c in unsupported"
          :key="c"
        >
          {{ c }}
        </li>
      </ul>
      <button
        type="submit"
        :disabled="!canSubmit"
      >
        {{ pending ? 'Finding the audience…' : 'Propose audience' }}
      </button>
      <p
        v-if="pending"
        role="status"
        class="muted"
      >
        The AI is reading the brief. This can take a few seconds.
      </p>
    </form>
  </section>
</template>

<style scoped>
label {
  display: block;
  font-weight: 600;
  margin-bottom: 0.4rem;
}
textarea {
  width: 100%;
  box-sizing: border-box;
  font: inherit;
  padding: 0.5rem;
  border: 1px solid #cbd5e1;
  border-radius: 6px;
}
.over {
  color: #991b1b;
  font-weight: 600;
}
</style>
