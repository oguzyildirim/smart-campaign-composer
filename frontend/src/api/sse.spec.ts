import { describe, expect, it } from 'vitest'
import { readSSE, type SSEMessage } from './sse'

function streamOf(chunks: string[]): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder()
  return new ReadableStream({
    start(controller) {
      for (const c of chunks) controller.enqueue(encoder.encode(c))
      controller.close()
    },
  })
}

async function collect(chunks: string[]): Promise<SSEMessage[]> {
  const out: SSEMessage[] = []
  await readSSE(streamOf(chunks), (m) => out.push(m))
  return out
}

describe('readSSE', () => {
  it('parses events, ignoring comments', async () => {
    const got = await collect([': hello\n\nevent: delta\ndata: {"a":1}\n\n: heartbeat\n\nevent: done\ndata: {}\n\n'])
    expect(got).toEqual([
      { event: 'delta', data: '{"a":1}' },
      { event: 'done', data: '{}' },
    ])
  })

  it('handles chunks split anywhere, including inside multi-byte characters', async () => {
    const text = 'event: delta\ndata: {"t":"Şişli İstanbul"}\r\n\r\n'
    const bytes = new TextEncoder().encode(text)
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        for (const b of bytes) controller.enqueue(new Uint8Array([b]))
        controller.close()
      },
    })
    const got: SSEMessage[] = []
    await readSSE(stream, (m) => got.push(m))
    expect(got).toEqual([{ event: 'delta', data: '{"t":"Şişli İstanbul"}' }])
  })

  it('does not dispatch an incomplete trailing event', async () => {
    expect(await collect(['event: done\ndata: {}'])).toEqual([])
  })

  it('defaults the event name and joins multi-line data', async () => {
    expect(await collect(['data: a\ndata: b\n\n'])).toEqual([{ event: 'message', data: 'a\nb' }])
  })
})
