<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import { MAX_APPROVER_CHARS, type Channel, type Counts } from '../api/campaigns'

// ApproveDialog asks for explicit confirmation and the operator's name.
// Confirm is disabled while a request is pending, so repeated clicks send
// one request; the parent also guards against re-entry.
const props = defineProps<{
  open: boolean
  pending: boolean
  error: string
  channels: Channel[]
  counts: Counts
}>()

const emit = defineEmits<{ confirm: [approvedBy: string]; cancel: [] }>()

const name = ref('')
const input = ref<HTMLInputElement | null>(null)

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
  const trimmed = name.value.trim()
  if (props.pending || trimmed === '') return
  emit('confirm', trimmed)
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
        recipients. Approved campaigns can no longer be edited.
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
            :disabled="pending || name.trim() === ''"
          >
            {{ pending ? 'Approving…' : 'Approve' }}
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
.dialog label {
  display: block;
  font-weight: 600;
  margin-bottom: 0.25rem;
}
.dialog input {
  width: 100%;
  box-sizing: border-box;
  font: inherit;
  padding: 0.45rem;
  border: 1px solid #cbd5e1;
  border-radius: 6px;
}
.actions {
  display: flex;
  justify-content: flex-end;
  gap: 0.5rem;
  margin-top: 1rem;
}
</style>
