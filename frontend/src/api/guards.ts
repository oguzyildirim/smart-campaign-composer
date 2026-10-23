// Small runtime checks for untrusted JSON. Each throws ShapeError when a
// value does not have the expected type, so a malformed response never
// reaches components half-parsed.

export class ShapeError extends Error {
  constructor(path: string) {
    super(`Unexpected response shape at ${path}`)
    this.name = 'ShapeError'
  }
}

export type Obj = Record<string, unknown>

export function asObject(value: unknown, path: string): Obj {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) throw new ShapeError(path)
  return value as Obj
}

export function asString(value: unknown, path: string): string {
  if (typeof value !== 'string') throw new ShapeError(path)
  return value
}

export function asNumber(value: unknown, path: string): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) throw new ShapeError(path)
  return value
}

export function asBoolean(value: unknown, path: string): boolean {
  if (typeof value !== 'boolean') throw new ShapeError(path)
  return value
}

export function asArray(value: unknown, path: string): unknown[] {
  if (!Array.isArray(value)) throw new ShapeError(path)
  return value
}

export function asStringArray(value: unknown, path: string): string[] {
  return asArray(value, path).map((v, i) => asString(v, `${path}[${i}]`))
}

export function asOneOf<T extends string>(value: unknown, allowed: readonly T[], path: string): T {
  const s = asString(value, path)
  if (!(allowed as readonly string[]).includes(s)) throw new ShapeError(path)
  return s as T
}

export function nullable<T>(value: unknown, parse: (v: unknown) => T): T | null {
  return value === null ? null : parse(value)
}
