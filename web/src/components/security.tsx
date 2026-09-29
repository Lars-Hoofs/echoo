import { useMutation, useQueryClient } from '@tanstack/react-query'
import qrcode from 'qrcode-generator'
import { Check, Copy, Download } from 'lucide-react'
import { type SubmitEvent, useMemo, useState } from 'react'

import { api } from '../lib/api'
import { errorMessage, fieldError } from '../lib/errors'
import { Button, ErrorNotice, Field, Input } from './ui'

export function ChangePasswordForm({ currentLabel = 'Huidig wachtwoord', onDone }: { currentLabel?: string; onDone: () => void }) {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const queryClient = useQueryClient()
  const change = useMutation({
    mutationFn: () => api('POST', '/me/password', { current_password: current, new_password: next }),
    onSuccess: async () => {
      setCurrent('')
      setNext('')
      await queryClient.invalidateQueries({ queryKey: ['me'] })
      onDone()
    },
  })
  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    change.mutate()
  }
  const generic = change.isError && !fieldError(change.error, 'current_password') && !fieldError(change.error, 'new_password')

  return (
    <form onSubmit={submit} className="flex max-w-sm flex-col gap-4" noValidate>
      {generic && <ErrorNotice>{errorMessage(change.error)}</ErrorNotice>}
      <Field label={currentLabel} error={fieldError(change.error, 'current_password')}>
        {(p) => <Input {...p} type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} />}
      </Field>
      <Field label="Nieuw wachtwoord" help="Minimaal 12 tekens. Een zin van een paar woorden werkt goed." error={fieldError(change.error, 'new_password')}>
        {(p) => <Input {...p} type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} />}
      </Field>
      <div>
        <Button type="submit" variant="primary" busy={change.isPending} disabled={!current || !next}>
          Wachtwoord wijzigen
        </Button>
      </div>
    </form>
  )
}

function QRCode({ value }: { value: string }) {
  const cells = useMemo(() => {
    const qr = qrcode(0, 'M')
    qr.addData(value)
    qr.make()
    const n = qr.getModuleCount()
    const dark: [number, number][] = []
    for (let r = 0; r < n; r++) for (let c = 0; c < n; c++) if (qr.isDark(r, c)) dark.push([c, r])
    return { n, dark }
  }, [value])
  const quiet = 4
  const size = cells.n + quiet * 2
  // Always dark on white: scanners expect it, regardless of the app theme.
  return (
    <svg viewBox={`0 0 ${size} ${size}`} width={176} height={176} role="img" className="shrink-0 rounded-md border border-line" aria-label="QR-code voor je authenticator-app" shapeRendering="crispEdges">
      <rect width={size} height={size} fill="#ffffff" />
      {cells.dark.map(([x, y]) => (
        <rect key={`${x}-${y}`} x={x + quiet} y={y + quiet} width={1} height={1} fill="#000000" />
      ))}
    </svg>
  )
}

export function RecoveryCodes({ codes }: { codes: string[] }) {
  const [copied, setCopied] = useState(false)
  const text = codes.join('\n')
  const download = () => {
    const url = URL.createObjectURL(new Blob([text + '\n'], { type: 'text/plain' }))
    const a = document.createElement('a')
    a.href = url
    a.download = 'echoo-herstelcodes.txt'
    a.click()
    URL.revokeObjectURL(url)
  }
  return (
    <div className="flex flex-col gap-3">
      <p className="text-base text-muted">
        Bewaar deze codes op een veilige plek. Je hebt ze nodig als je je telefoon kwijt bent. Elke code werkt één keer, en ze worden
        niet opnieuw getoond.
      </p>
      <ol className="grid grid-cols-2 gap-x-6 gap-y-1.5 rounded-md border border-line bg-subtle px-4 py-3 font-mono text-base tabular-nums">
        {codes.map((c) => (
          <li key={c}>{c}</li>
        ))}
      </ol>
      <div className="flex gap-2">
        <Button
          onClick={() => {
            void navigator.clipboard.writeText(text).then(() => {
              setCopied(true)
            })
          }}
        >
          {copied ? <Check size={14} aria-hidden /> : <Copy size={14} aria-hidden />}
          {copied ? 'Gekopieerd' : 'Kopiëren'}
        </Button>
        <Button onClick={download}>
          <Download size={14} aria-hidden />
          Downloaden
        </Button>
      </div>
    </div>
  )
}

interface Setup {
  secret: string
  uri: string
}

// onEnabled fires as soon as 2FA is on, while the recovery codes are still shown; onDone fires
// when the user confirms they saved them.
export function TotpEnrollment({ onEnabled, onDone }: { onEnabled?: () => void; onDone: () => void }) {
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [codes, setCodes] = useState<string[] | null>(null)
  const queryClient = useQueryClient()
  const begin = useMutation({ mutationFn: () => api<Setup>('POST', '/me/totp/setup') })
  const enable = useMutation({
    mutationFn: () => api<{ recovery_codes: string[] }>('POST', '/me/totp/enable', { password, code }),
    onSuccess: (r) => {
      setCodes(r.recovery_codes)
      onEnabled?.()
    },
  })

  if (codes) {
    return (
      <div className="flex max-w-md flex-col gap-4">
        <p className="text-base text-ink">Tweestapsverificatie staat aan. Andere sessies zijn afgemeld.</p>
        <RecoveryCodes codes={codes} />
        <div>
          <Button
            variant="primary"
            onClick={() => {
              void queryClient.invalidateQueries({ queryKey: ['me'] }).then(onDone)
            }}
          >
            Ik heb de codes bewaard
          </Button>
        </div>
      </div>
    )
  }

  if (!begin.data) {
    return (
      <div className="flex flex-col gap-3">
        {begin.isError && <ErrorNotice>{errorMessage(begin.error)}</ErrorNotice>}
        <div>
          <Button variant="primary" busy={begin.isPending} onClick={() => begin.mutate()}>
            Instellen
          </Button>
        </div>
      </div>
    )
  }

  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    enable.mutate()
  }
  const groupedSecret = begin.data.secret.match(/.{1,4}/g)?.join(' ') ?? begin.data.secret

  return (
    <form onSubmit={submit} className="flex max-w-md flex-col gap-4" noValidate>
      <p className="text-base text-muted">
        Scan de code met een authenticator-app (bijvoorbeeld 1Password, Bitwarden of Google Authenticator) en vul daarna de code in die
        de app toont.
      </p>
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
        <QRCode value={begin.data.uri} />
        <div className="text-sm text-muted">
          Scannen lukt niet? Vul deze sleutel handmatig in:
          <div className="mt-1 font-mono text-base break-all text-ink select-all">{groupedSecret}</div>
        </div>
      </div>
      {enable.isError && !fieldError(enable.error, 'password') && <ErrorNotice>{errorMessage(enable.error)}</ErrorNotice>}
      <Field label="Wachtwoord" error={fieldError(enable.error, 'password')}>
        {(p) => <Input {...p} type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />}
      </Field>
      <Field label="Verificatiecode">
        {(p) => (
          <Input
            {...p}
            inputMode="numeric"
            autoComplete="one-time-code"
            maxLength={6}
            className="max-w-[140px] font-mono tracking-[0.2em] tabular-nums"
            value={code}
            onChange={(e) => setCode(e.target.value.replace(/\D/g, ''))}
          />
        )}
      </Field>
      <div>
        <Button type="submit" variant="primary" busy={enable.isPending} disabled={!password || code.length !== 6}>
          Inschakelen
        </Button>
      </div>
    </form>
  )
}
