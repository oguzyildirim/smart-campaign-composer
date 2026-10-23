<script setup lang="ts">
import type { Preview } from '../api/campaigns'

// PreviewPanel shows the backend's rendering for real (fake) recipients
// with final character counts after personalization.
defineProps<{
  preview: Preview | null
  loading: boolean
  stale: boolean
  error: string
}>()

const fieldLabels: Record<string, string> = {
  'sms.body': 'SMS',
  'email.subject': 'Subject',
  'email.body': 'Body',
}
</script>

<template>
  <section
    class="panel"
    aria-labelledby="preview-heading"
    aria-live="polite"
    :aria-busy="loading"
  >
    <h2 id="preview-heading">
      Personalized preview
    </h2>
    <p
      v-if="error"
      class="error"
      role="alert"
    >
      {{ error }}
    </p>
    <p
      v-else-if="!preview"
      class="muted"
    >
      {{ loading ? 'Rendering preview…' : 'No preview yet.' }}
    </p>
    <template v-else>
      <p
        v-if="stale || loading"
        class="muted"
      >
        Updating preview for your latest changes…
      </p>
      <div
        v-if="preview.validation.valid"
        class="ok"
        data-testid="preview-valid"
      >
        ✓ Ready to approve: every check passed for this audience.
      </div>
      <div
        v-else
        class="error"
        data-testid="preview-invalid"
      >
        <strong>Not ready to approve:</strong>
        <ul>
          <li
            v-for="e in preview.validation.errors"
            :key="e.path + e.message"
          >
            {{ e.path }}: {{ e.message }}
          </li>
        </ul>
      </div>

      <div
        v-for="ch in preview.channels"
        :key="ch.channel"
        class="channel"
      >
        <h3>{{ ch.channel === 'sms' ? 'SMS' : 'Email' }} · {{ ch.recipients }} recipients</h3>
        <p class="muted">
          Longest first name {{ ch.longestFirstName }} characters, longest city {{ ch.longestCity }}.
        </p>
        <p
          v-if="ch.samples.length === 0"
          class="muted"
        >
          No consented recipients on this channel.
        </p>
        <article
          v-for="s in ch.samples"
          :key="s.userId"
          class="sample"
        >
          <p class="muted">
            For {{ s.firstName }} ({{ s.city }})
          </p>
          <div
            v-for="f in s.fields"
            :key="f.field"
            class="rendered"
          >
            <span class="rendered__label">{{ fieldLabels[f.field] ?? f.field }}</span>
            <pre class="rendered__text">{{ f.text }}</pre>
            <span
              class="rendered__count"
              :class="{ over: f.overLimit }"
              :data-testid="`final-${ch.channel}-${f.field}`"
            >
              {{ f.length }}/{{ f.limit }}<template v-if="f.overLimit"> — over the limit</template>
            </span>
          </div>
        </article>
      </div>
    </template>
  </section>
</template>

<style scoped>
.ok {
  color: #14532d;
  background: #dcfce7;
  border-radius: 6px;
  padding: 0.5rem 0.75rem;
}
.channel h3 {
  margin: 1rem 0 0.25rem;
  font-size: 1rem;
}
.sample {
  border-top: 1px solid #e2e8f0;
  padding: 0.5rem 0;
}
.rendered {
  margin: 0.25rem 0;
}
.rendered__label {
  font-size: 0.8rem;
  font-weight: 600;
}
.rendered__text {
  white-space: pre-wrap;
  font-family: inherit;
  margin: 0.15rem 0;
  background: #f1f5f9;
  padding: 0.4rem;
  border-radius: 4px;
}
.rendered__count {
  font-size: 0.8rem;
}
.rendered__count.over {
  color: #991b1b;
  font-weight: 600;
}
</style>
