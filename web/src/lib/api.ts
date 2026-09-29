export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(code)
  }
}

let csrfToken = ''

export function setCsrfToken(token: string) {
  csrfToken = token
}

interface ErrorBody {
  error?: { code?: string; fields?: Record<string, string> }
}

export async function api<T>(method: 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE', path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  if (method !== 'GET' && csrfToken) headers['X-CSRF-Token'] = csrfToken

  let res: Response
  try {
    res = await fetch('/api/v1' + path, {
      method,
      headers,
      credentials: 'same-origin',
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    })
  } catch {
    throw new ApiError(0, 'network')
  }

  if (res.status === 204) return undefined as T
  const data: unknown = await res.json().catch(() => ({}))
  if (!res.ok) {
    const err = (data as ErrorBody).error
    throw new ApiError(res.status, err?.code ?? 'internal', err?.fields ?? {})
  }
  const token = (data as { csrf_token?: unknown }).csrf_token
  if (typeof token === 'string') setCsrfToken(token)
  return data as T
}

// Uploads one file as multipart/form-data. XMLHttpRequest instead of fetch, because fetch cannot
// report upload progress.
export function apiUpload<T>(path: string, file: File, onProgress: (fraction: number) => void): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('POST', '/api/v1' + path)
    xhr.setRequestHeader('Accept', 'application/json')
    if (csrfToken) xhr.setRequestHeader('X-CSRF-Token', csrfToken)
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable) onProgress(e.loaded / e.total)
    }
    xhr.onerror = () => {
      reject(new ApiError(0, 'network'))
    }
    xhr.onload = () => {
      let data: unknown = {}
      try {
        data = JSON.parse(xhr.responseText)
      } catch {
        // A non-JSON body is reported through the status below.
      }
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(data as T)
        return
      }
      const err = (data as ErrorBody).error
      reject(new ApiError(xhr.status, err?.code ?? 'internal', err?.fields ?? {}))
    }
    const form = new FormData()
    form.append('file', file)
    xhr.send(form)
  })
}

// Posts a multipart form with fetch; the browser sets the boundary, so no Content-Type here.
export async function apiForm<T>(path: string, form: FormData): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (csrfToken) headers['X-CSRF-Token'] = csrfToken
  let res: Response
  try {
    res = await fetch('/api/v1' + path, { method: 'POST', headers, credentials: 'same-origin', body: form })
  } catch {
    throw new ApiError(0, 'network')
  }
  const data: unknown = await res.json().catch(() => ({}))
  if (!res.ok) {
    const err = (data as ErrorBody).error
    throw new ApiError(res.status, err?.code ?? 'internal', err?.fields ?? {})
  }
  return data as T
}
