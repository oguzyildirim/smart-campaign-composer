<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { RouterLink } from 'vue-router'
import { DELIVERY_STATUSES, type DeliveryStatus } from '../api/progress'
import { formatWithOffset } from '../api/time'
import ProgressBar from '../components/ProgressBar.vue'
import StatusBadge from '../components/StatusBadge.vue'
import { useCampaignProgress, type Connection } from '../composables/useCampaignProgress'

// Live delivery progress of one campaign. The backend sends full
// snapshots; this view only displays the latest one.
const props = defineProps<{ id: string }>()

const { progress, loadState, error, connection, load, reconnect } = useCampaignProgress(props.id)
onMounted(() => void load())

const statusLabels: Record<DeliveryStatus, string> = {
  queued: 'Queued',
  processing: 'Processing',
  sent: 'Sent',
  failed: 'Failed',
  retrying: 'Retrying',
}

const connectionLabels: Record<Connection, string> = {
  connecting: 'Connecting…',
  live: 'Live',
  reconnecting: 'Reconnecting…',
  disconnected: 'Disconnected',
  completed: 'Completed',
  ended: 'Ended',
}

const channelLabels = { sms: 'SMS', email: 'Email' } as const

const empty = computed(() => progress.value !== null && progress.value.totals.total === 0)
const perSecond = computed(() => (progress.value ? progress.value.throughput.perSecond.toFixed(1) : '0'))

function formatTime(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleTimeString()
}
</script>

<template>
  <section>
    <p>
      <RouterLink :to="`/campaigns/${id}`">
        ← Back to campaign
      </RouterLink>
    </p>
    <h2>Delivery dashboard</h2>

    <p
      v-if="loadState === 'loading'"
      role="status"
    >
      Loading progress…
    </p>
    <p
      v-else-if="loadState === 'error'"
      class="error"
      role="alert"
    >
      {{ error }}
    </p>

    <template v-else-if="progress">
      <div class="panel dashboard__header">
        <div>
          Campaign status:
          <span data-testid="campaign-status"><StatusBadge :status="progress.campaignStatus" /></span>
        </div>
        <div
          class="connection"
          :class="`connection--${connection}`"
          data-testid="connection"
          role="status"
          aria-live="polite"
        >
          <span
            class="connection__dot"
            aria-hidden="true"
          />
          {{ connectionLabels[connection] }}
          <button
            v-if="connection === 'disconnected'"
            type="button"
            class="secondary"
            data-testid="reconnect"
            @click="reconnect"
          >
            Reconnect
          </button>
        </div>
        <span class="muted">Updated {{ formatTime(progress.generatedAt) }}</span>
        <span
          v-if="progress.scheduledAt"
          data-testid="scheduled-banner"
        >
          Scheduled for <strong>{{ formatWithOffset(progress.scheduledAt) }}</strong>
        </span>
      </div>

      <div
        v-if="empty"
        class="panel"
        data-testid="empty"
      >
        <p v-if="progress.campaignStatus === 'scheduled'">
          No messages yet. The audience is built and the messages are queued when the campaign is released at the
          scheduled time.
        </p>
        <p v-else-if="progress.campaignStatus === 'release_failed'">
          Nothing was sent: the checks at release time failed. Open the campaign to see why.
        </p>
        <p v-else-if="progress.campaignStatus === 'expired'">
          Nothing was sent: the campaign was not released in time and expired.
        </p>
        <p v-else>
          No messages have been queued for this campaign yet. Deliveries are created when the campaign is approved.
        </p>
      </div>

      <template v-else>
        <div class="panel">
          <h3>Overall progress</h3>
          <ProgressBar
            :value="progress.percentDone"
            label="Messages sent or failed"
            data-testid="progress-total"
          />
          <p
            class="muted"
            data-testid="throughput"
          >
            {{ perSecond }} messages/second over the last {{ progress.throughput.windowSeconds }} seconds
            ({{ progress.throughput.sent }} sent)
          </p>
        </div>

        <div class="panel">
          <h3>Messages by channel and status</h3>
          <table class="counts">
            <caption class="visually-hidden">
              Delivery counts per channel and status
            </caption>
            <thead>
              <tr>
                <th scope="col">
                  Channel
                </th>
                <th
                  v-for="s in DELIVERY_STATUSES"
                  :key="s"
                  scope="col"
                >
                  {{ statusLabels[s] }}
                </th>
                <th scope="col">
                  Total
                </th>
                <th scope="col">
                  Progress
                </th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="ch in progress.channels"
                :key="ch.channel"
                data-testid="channel-row"
              >
                <th scope="row">
                  {{ channelLabels[ch.channel] }}
                </th>
                <td
                  v-for="s in DELIVERY_STATUSES"
                  :key="s"
                  :data-testid="`count-${ch.channel}-${s}`"
                >
                  {{ ch.counts[s] }}
                </td>
                <td :data-testid="`count-${ch.channel}-total`">
                  {{ ch.counts.total }}
                </td>
                <td>
                  <ProgressBar
                    :value="ch.percentDone"
                    :label="`${channelLabels[ch.channel]} messages sent or failed`"
                    :data-testid="`progress-${ch.channel}`"
                  />
                </td>
              </tr>
            </tbody>
            <tfoot>
              <tr>
                <th scope="row">
                  All
                </th>
                <td
                  v-for="s in DELIVERY_STATUSES"
                  :key="s"
                  :data-testid="`count-total-${s}`"
                >
                  {{ progress.totals[s] }}
                </td>
                <td data-testid="count-total-total">
                  {{ progress.totals.total }}
                </td>
                <td />
              </tr>
            </tfoot>
          </table>
        </div>

        <div
          class="panel"
          data-testid="failures"
        >
          <h3>Failures by reason</h3>
          <p
            v-if="progress.failures.length === 0"
            class="muted"
          >
            No failed messages.
          </p>
          <ul v-else>
            <li
              v-for="f in progress.failures"
              :key="f.code"
            >
              <code>{{ f.code }}</code>: {{ f.count }}
            </li>
          </ul>
        </div>
      </template>
    </template>
  </section>
</template>

<style scoped>
.dashboard__header {
  display: flex;
  flex-wrap: wrap;
  gap: 1.5rem;
  align-items: center;
}
.connection {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  font-weight: 600;
}
.connection__dot {
  width: 0.6rem;
  height: 0.6rem;
  border-radius: 50%;
  background: #94a3b8;
}
.connection--live .connection__dot {
  background: #16a34a;
}
.connection--reconnecting .connection__dot,
.connection--connecting .connection__dot {
  background: #ca8a04;
}
.connection--disconnected .connection__dot {
  background: #dc2626;
}
.counts {
  width: 100%;
  border-collapse: collapse;
  font-variant-numeric: tabular-nums;
}
.counts th,
.counts td {
  padding: 0.4rem 0.5rem;
  text-align: right;
  border-bottom: 1px solid #e2e8f0;
}
.counts th:first-child {
  text-align: left;
}
.counts td:last-child {
  min-width: 10rem;
}
.visually-hidden {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip: rect(0 0 0 0);
  white-space: nowrap;
}
</style>
