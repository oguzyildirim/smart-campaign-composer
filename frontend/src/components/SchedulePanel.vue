<script setup lang="ts">
import { computed, ref } from 'vue'
import { MAX_APPROVER_CHARS, type Campaign } from '../api/campaigns'
import { formatWithOffset, localInputToRFC3339 } from '../api/time'

// SchedulePanel shows a campaign approved for later (scheduled,
// release_failed, or expired) and offers what the backend allows in that
// status: reschedule while scheduled, cancel in all three. The backend
// decides; a release that already happened makes either request fail.
const props = defineProps<{
  campaign: Campaign
  pending: boolean
  error: string
}>()

const emit = defineEmits<{ reschedule: [actor: string, scheduledAt: string]; cancel: [actor: string] }>()

const actor = ref('')
const localTime = ref('')

const scheduled = computed(() => props.campaign.status === 'scheduled')
const newTime = computed(() => localInputToRFC3339(localTime.value))
const named = computed(() => actor.value.trim() !== '')

function formatDate(iso: string | null): string {
  return iso ? formatWithOffset(iso) : ''
}

function reschedule() {
  if (props.pending || !named.value || !newTime.value) return
  emit('reschedule', actor.value.trim(), newTime.value)
}

function cancel() {
  if (props.pending || !named.value) return
  emit('cancel', actor.value.trim())
}
</script>

<template>
  <section
    class="panel schedule"
    :class="{ 'schedule--problem': !scheduled }"
    aria-labelledby="schedule-heading"
    data-testid="schedule-panel"
  >
    <h2 id="schedule-heading">
      <template v-if="scheduled">
        Scheduled
      </template>
      <template v-else-if="campaign.status === 'release_failed'">
        Release failed
      </template>
      <template v-else>
        Expired
      </template>
    </h2>
    <p>
      Approved by {{ campaign.approvedBy }} on {{ formatDate(campaign.approvedAt) }} to send at
      <strong data-testid="scheduled-at">{{ formatDate(campaign.scheduledAt) }}</strong>.
    </p>
    <p v-if="scheduled">
      The content is locked. The audience is built and the messages are queued at that time; until then you can move
      or cancel it.
    </p>
    <div
      v-else-if="campaign.status === 'release_failed'"
      role="alert"
    >
      <p>
        Nothing was sent: the checks at release time failed.
        <template v-if="campaign.releaseFailure">
          {{ campaign.releaseFailure.error }}
        </template>
      </p>
      <ul v-if="campaign.releaseFailure && campaign.releaseFailure.details.length > 0">
        <li
          v-for="d in campaign.releaseFailure.details"
          :key="d.path + d.message"
        >
          <code>{{ d.path }}</code> {{ d.message }}
        </li>
      </ul>
      <p>Cancel to return the campaign to drafts, fix it, and approve it again.</p>
    </div>
    <p
      v-else
      role="alert"
    >
      Nothing was sent: the campaign was not released in time, so it expired instead of going out late. Cancel to
      return it to drafts and approve it again for a new time.
    </p>

    <form
      class="schedule__form"
      @submit.prevent
    >
      <label for="schedule-actor">Your name</label>
      <input
        id="schedule-actor"
        v-model="actor"
        type="text"
        autocomplete="name"
        :maxlength="MAX_APPROVER_CHARS"
      >
      <template v-if="scheduled">
        <label for="reschedule-at">New time (your local time)</label>
        <input
          id="reschedule-at"
          v-model="localTime"
          type="datetime-local"
        >
      </template>
      <div class="schedule__actions">
        <button
          v-if="scheduled"
          type="button"
          data-testid="reschedule"
          :disabled="pending || !named || !newTime"
          @click="reschedule"
        >
          Reschedule
        </button>
        <button
          type="button"
          class="secondary"
          data-testid="cancel-schedule"
          :disabled="pending || !named"
          @click="cancel"
        >
          Cancel campaign
        </button>
      </div>
    </form>
    <p
      v-if="error"
      class="error"
      role="alert"
      data-testid="schedule-error"
    >
      {{ error }}
    </p>
  </section>
</template>

<style scoped>
.schedule {
  border-left: 4px solid #6366f1;
}
.schedule--problem {
  border-left-color: #dc2626;
}
.schedule__form {
  display: grid;
  gap: 0.25rem;
  max-width: 24rem;
}
.schedule__form label {
  font-weight: 600;
  margin-top: 0.5rem;
}
.schedule__form input {
  font: inherit;
  padding: 0.45rem;
  border: 1px solid #cbd5e1;
  border-radius: 6px;
}
.schedule__actions {
  display: flex;
  gap: 0.5rem;
  margin-top: 0.75rem;
}
</style>
