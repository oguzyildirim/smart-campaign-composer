<script setup lang="ts">
import type { Audience } from '../api/campaigns'

defineProps<{ audience: Audience }>()
</script>

<template>
  <section
    class="panel"
    aria-labelledby="audience-heading"
  >
    <h2 id="audience-heading">
      Audience
    </h2>
    <ul class="audience__rule">
      <li
        v-for="line in audience.summary"
        :key="line"
      >
        {{ line }}
      </li>
    </ul>
    <dl class="audience__counts">
      <div>
        <dt>Matching users</dt>
        <dd data-testid="count-total">
          {{ audience.counts.total }}
        </dd>
      </div>
      <div>
        <dt>SMS (with consent)</dt>
        <dd data-testid="count-sms">
          {{ audience.counts.sms }}
        </dd>
      </div>
      <div>
        <dt>Email (with consent)</dt>
        <dd data-testid="count-email">
          {{ audience.counts.email }}
        </dd>
      </div>
    </dl>
    <p
      v-if="audience.explanation"
      class="muted"
    >
      {{ audience.explanation }}
    </p>
    <div
      v-if="audience.unsupportedCriteria.length > 0"
      class="warning"
      role="note"
      data-testid="unsupported"
    >
      <strong>Not applied:</strong> the audience rules cannot express these parts of the brief, so the audience is
      broader than asked:
      <ul>
        <li
          v-for="c in audience.unsupportedCriteria"
          :key="c"
        >
          {{ c }}
        </li>
      </ul>
    </div>
  </section>
</template>

<style scoped>
.audience__rule {
  margin: 0 0 0.75rem;
  padding-left: 1.25rem;
}
.audience__counts {
  display: flex;
  flex-wrap: wrap;
  gap: 1.5rem;
  margin: 0;
}
.audience__counts dt {
  font-size: 0.8rem;
  color: #64748b;
}
.audience__counts dd {
  margin: 0;
  font-size: 1.5rem;
  font-weight: 600;
}
</style>
