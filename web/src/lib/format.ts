const dateTime = new Intl.DateTimeFormat('nl-NL', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })
const dateTimeYear = new Intl.DateTimeFormat('nl-NL', { day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' })

export function formatDateTime(iso: string | null, now = new Date()): string {
  if (!iso) return '—'
  const d = new Date(iso)
  return (d.getFullYear() === now.getFullYear() ? dateTime : dateTimeYear).format(d)
}

// A compact description of a browser from its user agent, for the sessions list.
export function describeUserAgent(ua: string): string {
  const browser = /Edg\//.test(ua)
    ? 'Edge'
    : /Firefox\//.test(ua)
      ? 'Firefox'
      : /Chrome\//.test(ua)
        ? 'Chrome'
        : /Safari\//.test(ua)
          ? 'Safari'
          : 'Onbekende browser'
  const os = /Windows/.test(ua)
    ? 'Windows'
    : /iPhone|iPad/.test(ua)
      ? 'iOS'
      : /Mac OS X/.test(ua)
        ? 'macOS'
        : /Android/.test(ua)
          ? 'Android'
          : /Linux/.test(ua)
            ? 'Linux'
            : ''
  return os ? `${browser} op ${os}` : browser
}

const shortDate = new Intl.DateTimeFormat('nl-NL', { day: 'numeric', month: 'short' })
const shortDateYear = new Intl.DateTimeFormat('nl-NL', { day: 'numeric', month: 'short', year: 'numeric' })

// Compact relative time for list rows: "nu", "5 min", "3 u", "2 d", then a date.
export function formatRelative(iso: string | null, now = new Date()): string {
  if (!iso) return ''
  const d = new Date(iso)
  const minutes = Math.floor((now.getTime() - d.getTime()) / 60_000)
  if (minutes < 1) return 'nu'
  if (minutes < 60) return `${minutes} min`
  if (minutes < 24 * 60) return `${Math.floor(minutes / 60)} u`
  if (minutes < 7 * 24 * 60) return `${Math.floor(minutes / (24 * 60))} d`
  return (d.getFullYear() === now.getFullYear() ? shortDate : shortDateYear).format(d)
}

const oneDecimal = new Intl.NumberFormat('nl-NL', { maximumFractionDigits: 1 })

export function formatBytes(bytes: number): string {
  if (bytes < 1000) return `${bytes} B`
  if (bytes < 1_000_000) return `${oneDecimal.format(bytes / 1000)} kB`
  return `${oneDecimal.format(bytes / 1_000_000)} MB`
}

const numberFormats = new Map<number, Intl.NumberFormat>()

// A number in Dutch notation, split where ron's numerals switch to grey: 1284.5 with one decimal
// gives integer "1.284" and decimals ",5".
export function splitNumber(value: number, fractionDigits = 0): { integer: string; decimals: string } {
  let format = numberFormats.get(fractionDigits)
  if (!format) {
    format = new Intl.NumberFormat('nl-NL', { minimumFractionDigits: fractionDigits, maximumFractionDigits: fractionDigits })
    numberFormats.set(fractionDigits, format)
  }
  let integer = ''
  let decimals = ''
  for (const part of format.formatToParts(value)) {
    if (part.type === 'decimal' || part.type === 'fraction') decimals += part.value
    else integer += part.value
  }
  return { integer, decimals }
}
