import { onUnmounted, reactive, ref } from 'vue'
import { streamDrafts, type DraftField, type DraftsDone } from '../api/campaigns'
import { describeError, isAbort } from './errors'

// useDraftStream runs one draft generation at a time and exposes the text
// streamed so far for display. The stream is aborted on unmount, which
// cancels the model call on the server.
export function useDraftStream() {
  const texts = reactive<Record<DraftField, string>>({ sms_body: '', email_subject: '', email_body: '' })
  const streaming = ref(false)
  const error = ref('')
  let controller: AbortController | null = null

  function reset() {
    texts.sms_body = ''
    texts.email_subject = ''
    texts.email_body = ''
  }

  // start resolves with the stored drafts, or null on failure or abort.
  async function start(id: string): Promise<DraftsDone | null> {
    if (streaming.value) return null
    controller = new AbortController()
    streaming.value = true
    error.value = ''
    reset()
    let attempt = 0
    try {
      return await streamDrafts(
        id,
        {
          onDelta(delta) {
            // A repair attempt restarts the text; never mix attempts.
            if (delta.attempt !== attempt) {
              attempt = delta.attempt
              reset()
            }
            texts[delta.field] += delta.text
          },
        },
        controller.signal,
      )
    } catch (err) {
      if (!isAbort(err)) error.value = describeError(err)
      return null
    } finally {
      streaming.value = false
      controller = null
    }
  }

  function cancel() {
    controller?.abort()
  }

  onUnmounted(cancel)

  return { texts, streaming, error, start, cancel }
}
