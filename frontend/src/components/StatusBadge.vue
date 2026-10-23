<script setup lang="ts">
import type { CampaignStatus } from '../api/campaigns'

defineProps<{ status: CampaignStatus }>()

const labels: Record<CampaignStatus, string> = {
  draft: 'Draft',
  audience_ready: 'Audience ready',
  drafts_ready: 'Drafts ready',
  approved: 'Approved',
  sending: 'Sending',
  completed: 'Completed',
}
</script>

<template>
  <span
    class="status"
    :class="`status--${status}`"
    :data-status="status"
  >{{ labels[status] }}</span>
</template>

<style scoped>
.status {
  display: inline-block;
  padding: 0.1rem 0.6rem;
  border-radius: 999px;
  font-size: 0.8rem;
  background: #e2e8f0;
}
.status--drafts_ready {
  background: #dbeafe;
}
.status--approved,
.status--completed {
  background: #dcfce7;
  color: #14532d;
}
.status--sending {
  background: #fef9c3;
  color: #713f12;
}
</style>
