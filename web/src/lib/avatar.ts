export function initials(name: string): string {
  const words = name
    .replace(/<.*>/, '')
    .replace(/@.*$/, '')
    .split(/[\s._-]+/)
    .filter(Boolean)
  const first = words[0]
  if (!first) return '?'
  const last = words.length > 1 ? words[words.length - 1] : undefined
  const letter = (w: string) => Array.from(w)[0] ?? ''
  return (letter(first) + (last ? letter(last) : '')).toUpperCase()
}
