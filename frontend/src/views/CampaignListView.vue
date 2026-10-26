<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { holdsApproval, listCampaigns, type CampaignSummary } from '../api/campaigns'
import { formatWithOffset } from '../api/time'
import StatusBadge from '../components/StatusBadge.vue'
import { describeError, isAbort } from '../composables/errors'

const state = ref<'loading' | 'ready' | 'error'>('loading')
const error = ref('')
const campaigns = ref<CampaignSummary[]>([])
const controller = new AbortController()

onMounted(async () => {
  try {
    campaigns.value = await listCampaigns(controller.signal)
    state.value = 'ready'
  } catch (err) {
    if (isAbort(err)) return
    error.value = describeError(err)
    state.value = 'error'
  }
})

onUnmounted(() => controller.abort())

function formatDate(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}
</script>

<template>
  <section>
    <h2>Campaigns</h2>
    <p
      v-if="state === 'loading'"
      role="status"
    >
      Loading campaigns…
    </p>
    <p
      v-else-if="state === 'error'"
      class="error"
      role="alert"
    >
      {{ error }}
    </p>
    <div
      v-else-if="campaigns.length === 0"
      class="panel"
    >
      <p>No campaigns yet.</p>
      <RouterLink to="/campaigns/new">
        Describe your first campaign
      </RouterLink>
    </div>
    <ul
      v-else
      class="list"
    >
      <li
        v-for="c in campaigns"
        :key="c.id"
        class="panel"
      >
        <RouterLink
          :to="`/campaigns/${c.id}`"
          class="list__brief"
        >
          {{ c.brief }}
        </RouterLink>
        <div class="list__meta">
          <StatusBadge :status="c.status" />
          <span class="muted">{{ c.counts.total }} users · SMS {{ c.counts.sms }} · email {{ c.counts.email }}</span>
          <span class="muted">{{ formatDate(c.createdAt) }}</span>
          <span
            v-if="c.scheduledAt"
            :data-testid="`scheduled-${c.id}`"
          >Send at {{ formatWithOffset(c.scheduledAt) }}</span>
          <RouterLink
            v-if="holdsApproval(c.status)"
            :to="`/campaigns/${c.id}/dashboard`"
            :data-testid="`dashboard-${c.id}`"
          >
            Delivery dashboard
          </RouterLink>
        </div>
      </li>
    </ul>
  </section>
</template>

<style scoped>
.list {
  list-style: none;
  padding: 0;
}
.list__brief {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.list__meta {
  display: flex;
  flex-wrap: wrap;
  gap: 0.75rem;
  align-items: center;
  margin-top: 0.4rem;
}
</style>
