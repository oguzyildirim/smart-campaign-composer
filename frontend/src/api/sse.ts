// Minimal Server-Sent Events reader for a fetch response body. The draft
// stream is a POST response, which EventSource cannot read; reading it
// with fetch also means a dropped connection never reconnects on its own
// and silently starts another paid model call.

export interface SSEMessage {
  event: string
  data: string
}

// readSSE calls onMessage for each complete event until the stream ends.
// Comment lines (heartbeats) and fields other than event/data are ignored.
export async function readSSE(
  body: ReadableStream<Uint8Array>,
  onMessage: (message: SSEMessage) => void,
): Promise<void> {
  const reader = body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let event = 'message'
  let data: string[] = []

  const dispatchLine = (line: string) => {
    if (line === '') {
      if (data.length > 0) onMessage({ event, data: data.join('\n') })
      event = 'message'
      data = []
      return
    }
    if (line.startsWith(':')) return
    const colon = line.indexOf(':')
    const field = colon < 0 ? line : line.slice(0, colon)
    let value = colon < 0 ? '' : line.slice(colon + 1)
    if (value.startsWith(' ')) value = value.slice(1)
    if (field === 'event') event = value
    else if (field === 'data') data.push(value)
  }

  try {
    for (;;) {
      const { value, done } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      let newline = buffer.search(/\r\n|\r|\n/)
      while (newline >= 0) {
        // A trailing "\r" may be the first half of "\r\n": wait for more.
        if (newline === buffer.length - 1 && buffer.endsWith('\r')) break
        const sep = buffer.startsWith('\r\n', newline) ? 2 : 1
        dispatchLine(buffer.slice(0, newline))
        buffer = buffer.slice(newline + sep)
        newline = buffer.search(/\r\n|\r|\n/)
      }
    }
  } finally {
    reader.releaseLock()
  }
}
