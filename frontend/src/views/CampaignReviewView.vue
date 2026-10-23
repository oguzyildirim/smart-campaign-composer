<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import {
  approveCampaign,
  editCampaign,
  getCampaign,
  previewCampaign,
  type Campaign,
  type Channel,
  type Drafts,
  type Preview,
} from '../api/campaigns'
import ApproveDialog from '../components/ApproveDialog.vue'
import AudiencePanel from '../components/AudiencePanel.vue'
import DraftEditor from '../components/DraftEditor.vue'
import PreviewPanel from '../components/PreviewPanel.vue'
import StatusBadge from '../components/StatusBadge.vue'
import { describeError, isAbort } from '../composables/errors'
import { useDraftStream } from '../composables/useDraftStream'

// The backend owns status, validation, and approval: this view sends the
// marketer's edits, shows what the backend reports, and approves only the
// version and content hash it last received.
const props = defineProps<{ id: string }>()

// SAVE_DELAY_MS debounces autosave; the preview refreshes after each save.
const SAVE_DELAY_MS = 600

const campaign = ref<Campaign | null>(null)
const loadState = ref<'loading' | 'ready' | 'error'>('loading')
const loadError = ref('')

const drafts = ref<Drafts>({ sms_body: '', email_subject: '', email_body: '' })
const channels = ref<Channel[]>([])
const dirty = ref(false)
const saving = ref(false)
const saveError = ref('')

const preview = ref<Preview | null>(null)
const previewLoading = ref(false)
const previewError = ref('')

const dialogOpen = ref(false)
const approving = ref(false)
const approveError = ref('')

const stream = useDraftStream()
const controller = new AbortController()

let editSeq = 0
let saveTimer: ReturnType<typeof setTimeout> | undefined
let saveQueued = false
let previewSeq = 0

const readonly = computed(() => campaign.value?.status === 'approved')
const editable = computed(() => campaign.value?.status === 'drafts_ready' && !stream.streaming.value)
const previewCurrent = computed(
  () => preview.value !== null && campaign.value !== null && preview.value.version === campaign.value.version,
)
const validation = computed(() => (previewCurrent.value ? preview.value?.validation : campaign.value?.validation) ?? null)
const canApprove = computed(
  () =>
    editable.value &&
    !dirty.value &&
    !saving.value &&
    !approving.value &&
    previewCurrent.value &&
    preview.value?.validation.valid === true,
)

function syncForm(c: Campaign) {
  if (c.drafts) drafts.value = { ...c.drafts }
  channels.value = [...c.channels]
  dirty.value = false
}

async function load() {
  try {
    const c = await getCampaign(props.id, controller.signal)
    campaign.value = c
    syncForm(c)
    loadState.value = 'ready'
    if (c.drafts) void loadPreview()
  } catch (err) {
    if (isAbort(err)) return
    loadError.value = describeError(err)
    loadState.value = 'error'
  }
}

async function loadPreview() {
  const seq = ++previewSeq
  previewLoading.value = true
  try {
    const p = await previewCampaign(props.id, controller.signal)
    if (seq !== previewSeq) return // a newer request is in flight
    preview.value = p
    previewError.value = ''
  } catch (err) {
    if (isAbort(err) || seq !== previewSeq) return
    previewError.value = describeError(err)
  } finally {
    if (seq === previewSeq) previewLoading.value = false
  }
}

function onEdit() {
  if (!editable.value) return
  editSeq++
  dirty.value = true
  clearTimeout(saveTimer)
  saveTimer = setTimeout(() => void save(), SAVE_DELAY_MS)
}

function updateDrafts(d: Drafts) {
  drafts.value = d
  onEdit()
}

function updateChannels(c: Channel[]) {
  channels.value = c
  onEdit()
}

async function save() {
  const c = campaign.value
  if (!c || !editable.value) return
  if (saving.value) {
    saveQueued = true
    return
  }
  saving.value = true
  const seq = editSeq
  try {
    const updated = await editCampaign(
      props.id,
      { version: c.version, channels: channels.value, ...drafts.value },
      controller.signal,
    )
    campaign.value = updated
    if (seq === editSeq) dirty.value = false
    saveError.value = ''
    void loadPreview()
  } catch (err) {
    if (isAbort(err)) return
    saveError.value = describeError(err)
  } finally {
    saving.value = false
    if (saveQueued) {
      saveQueued = false
      void save()
    }
  }
}

async function generate() {
  const done = await stream.start(props.id)
  if (!done) return
  // Reload the authoritative campaign rather than assembling it locally.
  await load()
}

function openApprove() {
  if (!canApprove.value) return
  approveError.value = ''
  dialogOpen.value = true
}

async function approve(approvedBy: string) {
  const c = campaign.value
  if (!c || approving.value) return
  approving.value = true
  approveError.value = ''
  try {
    const approved = await approveCampaign(
      props.id,
      { version: c.version, content_hash: c.contentHash, approved_by: approvedBy },
      controller.signal,
    )
    campaign.value = approved
    syncForm(approved)
    dialogOpen.value = false
  } catch (err) {
    if (isAbort(err)) return
    approveError.value = describeError(err)
  } finally {
    approving.value = false
  }
}

onMounted(load)

onUnmounted(() => {
  clearTimeout(saveTimer)
  controller.abort()
})

function formatDate(iso: string | null): string {
  if (!iso) return ''
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}
</script>

<template>
  <p
    v-if="loadState === 'loading'"
    role="status"
  >
    Loading campaign…
  </p>
  <p
    v-else-if="loadState === 'error'"
    class="error"
    role="alert"
  >
    {{ loadError }}
  </p>
  <div
    v-else-if="campaign"
    class="review"
  >
    <header class="review__header">
      <h2>Review campaign</h2>
      <StatusBadge :status="campaign.status" />
    </header>
    <p class="review__brief">
      <strong>Brief:</strong> {{ campaign.brief }}
    </p>

    <div
      v-if="readonly"
      class="ok"
      role="status"
      data-testid="approved-banner"
    >
      Approved by {{ campaign.approvedBy }} on {{ formatDate(campaign.approvedAt) }}. This campaign can no longer be
      edited.
    </div>

    <AudiencePanel :audience="campaign.audience" />

    <section
      v-if="campaign.status === 'audience_ready' || stream.streaming.value"
      class="panel"
      aria-labelledby="gen-heading"
    >
      <h2 id="gen-heading">
        Drafts
      </h2>
      <p
        v-if="campaign.generating && !stream.streaming.value"
        class="muted"
      >
        Drafts are already being generated for this campaign.
      </p>
      <button
        v-if="!stream.streaming.value"
        type="button"
        data-testid="generate"
        :disabled="campaign.generating"
        @click="generate"
      >
        Generate SMS and email drafts
      </button>
      <button
        v-else
        type="button"
        class="secondary"
        @click="stream.cancel()"
      >
        Stop
      </button>
      <p
        v-if="stream.error.value"
        class="error"
        role="alert"
        data-testid="stream-error"
      >
        {{ stream.error.value }}
      </p>
      <div
        v-if="stream.streaming.value"
        class="streaming"
        aria-live="polite"
        data-testid="streaming"
      >
        <h3>SMS</h3>
        <pre data-testid="stream-sms_body">{{ stream.texts.sms_body }}</pre>
        <h3>Email subject</h3>
        <pre data-testid="stream-email_subject">{{ stream.texts.email_subject }}</pre>
        <h3>Email body</h3>
        <pre data-testid="stream-email_body">{{ stream.texts.email_body }}</pre>
      </div>
    </section>

    <template v-if="campaign.drafts && !stream.streaming.value">
      <div class="review__grid">
        <div>
          <DraftEditor
            :drafts="drafts"
            :channels="channels"
            :readonly="!editable"
            :errors="validation?.errors ?? []"
            :worst-case="validation?.worstCaseLengths ?? null"
            :limits="validation?.limits ?? null"
            @update:drafts="updateDrafts"
            @update:channels="updateChannels"
          />
          <p
            class="muted"
            role="status"
            data-testid="save-state"
          >
            <template v-if="saving">
              Saving…
            </template>
            <template v-else-if="dirty">
              Unsaved changes
            </template>
            <template v-else-if="!readonly">
              All changes saved
            </template>
          </p>
          <p
            v-if="saveError"
            class="error"
            role="alert"
            data-testid="save-error"
          >
            {{ saveError }}
          </p>
        </div>
        <PreviewPanel
          :preview="preview"
          :loading="previewLoading"
          :stale="dirty || saving || !previewCurrent"
          :error="previewError"
        />
      </div>

      <div
        v-if="campaign.status === 'drafts_ready'"
        class="review__actions"
      >
        <button
          type="button"
          data-testid="approve"
          :disabled="!canApprove"
          @click="openApprove"
        >
          Approve…
        </button>
        <span
          v-if="!canApprove"
          class="muted"
        >
          Approval is available once all changes are saved and the preview passes every check.
        </span>
      </div>
    </template>

    <ApproveDialog
      :open="dialogOpen"
      :pending="approving"
      :error="approveError"
      :channels="campaign.channels"
      :counts="campaign.audience.counts"
      @confirm="approve"
      @cancel="dialogOpen = false"
    />
  </div>
</template>

<style scoped>
.review__header {
  display: flex;
  align-items: center;
  gap: 0.75rem;
}
.review__grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
  gap: 1rem;
}
.review__actions {
  display: flex;
  align-items: center;
  gap: 1rem;
}
.streaming pre {
  white-space: pre-wrap;
  font-family: inherit;
  background: #f1f5f9;
  padding: 0.5rem;
  border-radius: 4px;
  min-height: 1.5rem;
}
.ok {
  color: #14532d;
  background: #dcfce7;
  border-radius: 6px;
  padding: 0.5rem 0.75rem;
  margin-bottom: 1rem;
}
</style>
