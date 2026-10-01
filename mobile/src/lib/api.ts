// The Echoo API as the web app uses it: a session cookie (kept by the platform's cookie store),
// the CSRF token from /auth/login or /me on every non-GET request, and the installation's origin
// in the Origin header, which the server checks on state-changing requests.

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message)
  }
}

let baseUrl = ''
let csrfToken = ''
let onUnauthenticated: () => void = () => undefined

export function setServer(url: string) {
  baseUrl = url
}

export function server(): string {
  return baseUrl
}

export function setCsrfToken(token: string) {
  csrfToken = token
}

export function setUnauthenticatedHandler(fn: () => void) {
  onUnauthenticated = fn
}

export async function api<T>(method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE', path: string, body?: unknown): Promise<T> {
  if (!baseUrl) throw new ApiError(0, 'no_server', 'no server configured')
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  if (method !== 'GET') {
    headers['X-CSRF-Token'] = csrfToken
    headers.Origin = baseUrl
  }
  let res: Response
  try {
    res = await fetch(`${baseUrl}/api/v1${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: 'include',
    })
  } catch {
    throw new ApiError(0, 'network', 'the server cannot be reached')
  }
  if (res.status === 204) return undefined as T
  const data: unknown = await res.json().catch(() => null)
  if (!res.ok) {
    const err = (data as { error?: { code?: string; message?: string; fields?: Record<string, string> } } | null)?.error
    if (res.status === 401 && path !== '/auth/login' && path !== '/auth/mfa') onUnauthenticated()
    throw new ApiError(res.status, err?.code ?? 'internal', err?.message ?? `request failed with ${res.status}`, err?.fields)
  }
  return data as T
}

// Texts for the errors an agent can act on; everything else is one generic line.
// The same wording as web/src/lib/errors.ts.
const messages: Record<string, string> = {
  network: 'Geen verbinding met Echoo. Controleer je netwerk en probeer het opnieuw.',
  invalid_credentials: 'Inloggen mislukt. Controleer je e-mailadres en wachtwoord, of probeer het over 15 minuten opnieuw.',
  invalid_code: 'Deze code klopt niet of is al gebruikt.',
  rate_limited: 'Te veel pogingen. Wacht een minuut en probeer het opnieuw.',
  sso_required: 'Inloggen met een wachtwoord staat uit. Gebruik single sign-on.',
  forbidden: 'Je hebt geen rechten voor deze actie.',
  not_found: 'Dit bestaat niet (meer).',
  version_conflict: 'Dit gesprek is intussen door iemand anders gewijzigd. Het is vernieuwd.',
  assignee_no_access: 'Deze persoon heeft geen schrijfrechten op de mailbox van dit gesprek.',
  not_snoozable: 'Gesloten gesprekken en spam kun je niet uitstellen.',
  mailbox_disabled: 'Deze mailbox staat uit en kan geen berichten versturen.',
  too_late: 'Te laat: het bericht wordt al verzonden en kan niet meer worden teruggehaald.',
  idempotency_conflict: 'Dit bericht kon niet worden verstuurd. Probeer het opnieuw.',
  validation_failed: 'Controleer wat je hebt ingevuld.',
}

export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) return messages[err.code] ?? 'Er ging iets mis. Probeer het opnieuw.'
  return 'Er ging iets mis. Probeer het opnieuw.'
}
