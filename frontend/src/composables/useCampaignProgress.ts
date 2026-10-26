import { onUnmounted, ref } from 'vue'
import { getProgress, parseProgress, progressEventsURL, type Progress } from '../api/progress'
import { describeError, isAbort } from './errors'

// Connection states shown on the dashboard.
// connecting: before the stream opened; live: receiving updates;
// reconnecting: the browser is re-establishing a dropped stream;
// disconnected: the server refused the stream (retry by hand);
// completed: the campaign finished, no stream is needed;
// ended: the campaign expired without sending, no stream is needed.
export type Connection = 'connecting' | 'live' | 'reconnecting' | 'disconnected' | 'completed' | 'ended'

// finalConnection is the state for a campaign whose progress can no
// longer change, or null while it can.
function finalConnection(p: Progress): Connection | null {
  if (p.campaignStatus === 'completed') return 'completed'
  if (p.campaignStatus === 'expired') return 'ended'
  return null
}

// useCampaignProgress loads a campaign's progress, then follows it live
// over EventSource. Each event carries the full state and replaces the
// shown snapshot, so reconnects and repeated events never double-count.
// The stream is closed on completion or expiry and on unmount. A
// scheduled campaign streams too, so its release shows up without a
// reload.
export function useCampaignProgress(id: string) {
  const progress = ref<Progress | null>(null)
  const loadState = ref<'loading' | 'ready' | 'error'>('loading')
  const error = ref('')
  const connection = ref<Connection>('connecting')

  const controller = new AbortController()
  let source: EventSource | null = null

  function apply(data: string): Progress | null {
    try {
      const p = parseProgress(JSON.parse(data))
      progress.value = p
      return p
    } catch {
      return null // malformed event: keep the last valid state
    }
  }

  function close() {
    source?.close()
    source = null
  }

  function connect() {
    close()
    connection.value = 'connecting'
    const es = new EventSource(progressEventsURL(id))
    source = es
    es.onopen = () => {
      if (source === es) connection.value = 'live'
    }
    es.onerror = () => {
      if (source !== es) return
      // CONNECTING: the browser retries on its own and resumes with a
      // fresh snapshot. CLOSED: it gave up (e.g. 404 or 429).
      connection.value = es.readyState === EventSource.CLOSED ? 'disconnected' : 'reconnecting'
      if (es.readyState === EventSource.CLOSED) close()
    }
    es.addEventListener('snapshot', (ev) => {
      if (source === es) apply(ev.data)
    })
    es.addEventListener('complete', (ev) => {
      if (source !== es) return
      const p = apply(ev.data)
      close()
      connection.value = (p && finalConnection(p)) ?? 'completed'
    })
  }

  async function load() {
    try {
      const p = await getProgress(id, controller.signal)
      progress.value = p
      loadState.value = 'ready'
      const final = finalConnection(p)
      if (final) {
        connection.value = final
        return
      }
      if (!controller.signal.aborted) connect()
    } catch (err) {
      if (isAbort(err)) return
      error.value = describeError(err)
      loadState.value = 'error'
    }
  }

  onUnmounted(() => {
    controller.abort()
    close()
  })

  return { progress, loadState, error, connection, load, reconnect: connect }
}
