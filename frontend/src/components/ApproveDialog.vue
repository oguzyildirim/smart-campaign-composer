<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { MAX_APPROVER_CHARS, type Channel, type Counts } from '../api/campaigns'
import { localInputToRFC3339 } from '../api/time'

// ApproveDialog asks for explicit confirmation, the operator's name, and
// whether to send now or at a scheduled local time (sent to the backend
// with its UTC offset; the backend checks it). Confirm is disabled while
// a request is pending, so repeated clicks send one request; the parent
// also guards against re-entry.
const props = defineProps<{
  open: boolean
  pending: boolean
  error: string
  channels: Channel[]
  counts: Counts
}>()

// confirm's scheduledAt is RFC 3339 with offset, or undefined to send now.
const emit = defineEmits<{ confirm: [approvedBy: string, scheduledAt: string | undefined]; cancel: [] }>()

const name = ref('')
const when = ref<'now' | 'schedule'>('now')
const localTime = ref('')
const input = ref<HTMLInputElement | null>(null)

const scheduledAt = computed(() => (when.value === 'schedule' ? localInputToRFC3339(localTime.value) : null))
const ready = computed(() => name.value.trim() !== '' && (when.value === 'now' || scheduledAt.value !== null))

watch(
  () => props.open,
  async (open) => {
    if (open) {
      await nextTick()
      input.value?.focus()
    }
  },
)

function confirm() {
  if (props.pending || !ready.value) return
  emit('confirm', name.value.trim(), when.value === 'schedule' ? (scheduledAt.value ?? undefined) : undefined)
}
</script>

<template>
  <div
    v-if="open"
    class="backdrop"
    @keydown.esc="emit('cancel')"
  >
    <div
      class="dialog"
      role="dialog"
      aria-modal="true"
      aria-labelledby="approve-title"
    >
      <h2 id="approve-title">
        Approve campaign
      </h2>
      <p>
        You approve exactly the content and audience shown on this page for
        <strong>{{ channels.map((c) => (c === 'sms' ? `SMS (${counts.sms})` : `email (${counts.email})`)).join(' and ') }}</strong>
        recipients<template v-if="when === 'schedule'">
          (an estimate: the audience is built again at the scheduled time)
        </template>. Approved campaigns can no longer be
        edited.
      </p>
      <form @submit.prevent="confirm">
        <label for="approver-name">Your name</label>
        <input
          id="approver-name"
          ref="input"
          v-model="name"
          type="text"
          autocomplete="name"
          :maxlength="MAX_APPROVER_CHARS"
          required
        >
        <fieldset class="when">
          <legend>When</legend>
          <label>
            <input
              v-model="when"
              type="radio"
              value="now"
              data-testid="when-now"
            >
            Send now
          </label>
          <label>
            <input
              v-model="when"
              type="radio"
              value="schedule"
              data-testid="when-schedule"
            >
            Schedule
          </label>
        </fieldset>
        <template v-if="when === 'schedule'">
          <label for="scheduled-at">Send at (your local time)</label>
          <input
            id="scheduled-at"
            v-model="localTime"
            type="datetime-local"
            required
          >
        </template>
        <p
          v-if="error"
          class="error"
          role="alert"
        >
          {{ error }}
        </p>
        <div class="actions">
          <button
            type="button"
            class="secondary"
            :disabled="pending"
            @click="emit('cancel')"
          >
            Cancel
          </button>
          <button
            type="submit"
            data-testid="confirm-approve"
            :disabled="pending || !ready"
          >
            {{ pending ? 'Approving…' : when === 'schedule' ? 'Schedule' : 'Approve' }}
          </button>
        </div>
      </form>
    </div>
  </div>
</template>

<style scoped>
.backdrop {
  position: fixed;
  inset: 0;
  background: rgb(15 23 42 / 0.4);
  display: grid;
  place-items: center;
  padding: 1rem;
}
.dialog {
  background: #fff;
  border-radius: 8px;
  padding: 1.25rem 1.5rem;
  max-width: 480px;
  width: 100%;
}
.dialog > form > label {
  display: block;
  font-weight: 600;
  margin: 0.5rem 0 0.25rem;
}
.dialog input[type='text'],
.dialog input[type='datetime-local'] {
  width: 100%;
  box-sizing: border-box;
  font: inherit;
  padding: 0.45rem;
  border: 1px solid #cbd5e1;
  border-radius: 6px;
}
.when {
  border: none;
  padding: 0;
  margin: 0.75rem 0 0;
  display: flex;
  gap: 1rem;
}
.when legend {
  font-weight: 600;
  padding: 0;
  margin-bottom: 0.25rem;
}
.actions {
  display: flex;
  justify-content: flex-end;
  gap: 0.5rem;
  margin-top: 1rem;
}
</style>
