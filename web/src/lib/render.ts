export interface PhishingWarning {
  kind: string
  detail: string
}

const HEIGHT_MESSAGE = 'echoo:render-height'
const MAX_FRAME_HEIGHT = 50_000

/*
 * The mail iframe is sandboxed without allow-same-origin, so its messages carry an opaque
 * origin and origin checks are useless. What can be verified is the sender window (it must be
 * this frame's contentWindow) and the payload shape. Anything else is ignored.
 */
export function frameHeight(event: { source: unknown; data: unknown }, frame: Window | null | undefined): number | undefined {
  if (!frame || event.source !== frame) return undefined
  const data = event.data
  if (typeof data !== 'object' || data === null) return undefined
  const { type, height } = data as { type?: unknown; height?: unknown }
  if (type !== HEIGHT_MESSAGE || typeof height !== 'number' || !Number.isFinite(height) || height < 0) return undefined
  return Math.min(Math.ceil(height), MAX_FRAME_HEIGHT)
}

// detail is sender-controlled text; it is only ever rendered as a React text node.
export function warningText({ kind, detail }: PhishingWarning): string {
  switch (kind) {
    case 'display_name_spoof':
      return `De getoonde naam verwijst naar ${detail}, maar het bericht komt van een ander adres.`
    case 'reply_to_mismatch':
      return `Antwoorden gaan naar ${detail} en niet naar het adres van de afzender.`
    case 'punycode_domain':
      return `Het domein van de afzender gebruikt bijzondere tekens (${detail}). Controleer of dit klopt.`
    case 'mixed_script_domain':
      return `Het domein van de afzender (${detail}) mengt letters uit verschillende alfabetten. Dit wordt gebruikt om bekende adressen na te bootsen.`
    case 'auth_failed':
      return `De mailserver kon de afzender niet bevestigen (${detail.toUpperCase()} mislukt).`
    case 'link_mismatch': {
      const [shown, actual] = detail.split(' -> ')
      return shown && actual ? `Een link toont ${shown}, maar leidt naar ${actual}.` : 'Een link leidt naar een andere site dan de tekst suggereert.'
    }
    default:
      return 'Dit bericht heeft een kenmerk dat vaak bij misleiding voorkomt.'
  }
}
