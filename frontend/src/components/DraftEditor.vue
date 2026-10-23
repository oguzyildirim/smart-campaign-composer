<script setup lang="ts">
import { computed } from 'vue'
import type { FieldError } from '../api/client'
import { CHANNELS, type Channel, type DraftField, type Drafts } from '../api/campaigns'

// DraftEditor edits message content and the channel selection. Character
// counts here are usability hints on the raw template; the preview shows
// the backend's final counts after personalization, and the backend
// decides validity.
const drafts = defineModel<Drafts>('drafts', { required: true })
const channels = defineModel<Channel[]>('channels', { required: true })

const props = defineProps<{
  readonly: boolean
  errors: FieldError[]
  worstCase: Record<DraftField, number> | null
  // limits come from the backend's validation; null until drafts exist.
  limits: Record<DraftField, number> | null
}>()

interface FieldSpec {
  key: DraftField
  label: string
  channel: Channel
  path: string
  multiline: boolean
}

const fields: FieldSpec[] = [
  { key: 'sms_body', label: 'SMS text', channel: 'sms', path: 'sms.body', multiline: true },
  { key: 'email_subject', label: 'Email subject', channel: 'email', path: 'email.subject', multiline: false },
  { key: 'email_body', label: 'Email body (plain text)', channel: 'email', path: 'email.body', multiline: true },
]

const channelLabels: Record<Channel, string> = { sms: 'SMS', email: 'Email' }

const errorsByPath = computed(() => {
  const out: Record<string, string[]> = {}
  for (const e of props.errors) (out[e.path] ??= []).push(e.message)
  return out
})

function chars(text: string): number {
  return Array.from(text).length
}

function setField(key: DraftField, value: string) {
  drafts.value = { ...drafts.value, [key]: value }
}

function toggleChannel(c: Channel, on: boolean) {
  const next = on ? [...channels.value, c] : channels.value.filter((x) => x !== c)
  channels.value = CHANNELS.filter((x) => next.includes(x))
}
</script>

<template>
  <section
    class="panel"
    aria-labelledby="content-heading"
  >
    <h2 id="content-heading">
      Content
    </h2>
    <fieldset
      class="channels"
      :disabled="readonly"
    >
      <legend>Channels</legend>
      <label
        v-for="c in CHANNELS"
        :key="c"
      >
        <input
          type="checkbox"
          :name="`channel-${c}`"
          :checked="channels.includes(c)"
          @change="toggleChannel(c, ($event.target as HTMLInputElement).checked)"
        >
        {{ channelLabels[c] }}
      </label>
    </fieldset>
    <p
      v-for="msg in errorsByPath['channels'] ?? []"
      :key="msg"
      class="error"
    >
      {{ msg }}
    </p>

    <div
      v-for="f in fields"
      :key="f.key"
      class="field"
      :class="{ 'field--off': !channels.includes(f.channel) }"
    >
      <label :for="`field-${f.key}`">
        {{ f.label }}
        <span
          v-if="!channels.includes(f.channel)"
          class="muted"
        >(channel not selected)</span>
      </label>
      <textarea
        v-if="f.multiline"
        :id="`field-${f.key}`"
        :value="drafts[f.key]"
        :readonly="readonly"
        :rows="f.key === 'email_body' ? 8 : 3"
        :aria-describedby="`hint-${f.key}`"
        :aria-invalid="(errorsByPath[f.path]?.length ?? 0) > 0"
        @input="setField(f.key, ($event.target as HTMLTextAreaElement).value)"
      />
      <input
        v-else
        :id="`field-${f.key}`"
        type="text"
        :value="drafts[f.key]"
        :readonly="readonly"
        :aria-describedby="`hint-${f.key}`"
        :aria-invalid="(errorsByPath[f.path]?.length ?? 0) > 0"
        @input="setField(f.key, ($event.target as HTMLInputElement).value)"
      >
      <p
        :id="`hint-${f.key}`"
        class="muted"
        :data-testid="`count-${f.key}`"
      >
        {{ chars(drafts[f.key]) }} characters as written
        <template v-if="worstCase">
          · up to {{ worstCase[f.key] }} after personalization
        </template>
        <template v-if="limits">
          · limit {{ limits[f.key] }}
        </template>
      </p>
      <p
        v-for="msg in errorsByPath[f.path] ?? []"
        :key="msg"
        class="error"
        role="alert"
      >
        {{ msg }}
      </p>
    </div>
    <p class="muted">
      Variables: <code v-pre>{{first_name}}</code>, <code v-pre>{{city}}</code>. Plain text only.
    </p>
  </section>
</template>

<style scoped>
.channels {
  border: none;
  padding: 0;
  margin: 0 0 1rem;
  display: flex;
  gap: 1rem;
}
.channels legend {
  font-weight: 600;
  margin-bottom: 0.25rem;
}
.field {
  margin-bottom: 1rem;
}
.field--off {
  opacity: 0.7;
}
.field label {
  display: block;
  font-weight: 600;
  margin-bottom: 0.25rem;
}
.field textarea,
.field input[type='text'] {
  width: 100%;
  box-sizing: border-box;
  font: inherit;
  padding: 0.5rem;
  border: 1px solid #cbd5e1;
  border-radius: 6px;
}
.field p {
  margin: 0.25rem 0 0;
}
</style>
