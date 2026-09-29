// Only same-site relative paths are accepted as a return target, to prevent open redirects
// such as ?terug=//evil.example or ?terug=/\evil.example.
export function safeReturnPath(v: unknown): string | undefined {
  return typeof v === 'string' && v.startsWith('/') && !v.startsWith('//') && !v.startsWith('/\\') ? v : undefined
}
