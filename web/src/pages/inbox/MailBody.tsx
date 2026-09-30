import { useMutation, useQueryClient } from '@tanstack/react-query'
import { ImageOff, TriangleAlert } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'

import { Button, ErrorNotice } from '../../components/ui'
import { api } from '../../lib/api'
import { errorMessage } from '../../lib/errors'
import type { Message } from '../../lib/inbox'
import { frameHeight, type PhishingWarning, warningText } from '../../lib/render'

type ImageScope = 'once' | 'sender' | 'domain'

export function PhishingNotice({ warnings }: { warnings: PhishingWarning[] }) {
  if (warnings.length === 0) return null
  return (
    <div className="flex items-start gap-3 rounded-md border border-danger-text/30 bg-danger-subtle px-4 py-3 text-base text-ink" role="note">
      <TriangleAlert size={16} aria-hidden className="mt-1 shrink-0 text-danger-text" />
      <div className="min-w-0">
        <p className="font-medium">Wees voorzichtig met links en bijlagen in dit bericht.</p>
        <ul className="mt-1 list-disc pl-4 wrap-anywhere">
          {warnings.map((w) => (
            <li key={`${w.kind}:${w.detail}`}>{warningText(w)}</li>
          ))}
        </ul>
      </div>
    </div>
  )
}

/*
 * Mail HTML never enters this document. It is served sanitized from /render/messages/{id} and
 * shown in an iframe with an opaque origin (sandbox without allow-same-origin), so even markup
 * that slipped through cannot reach the app, its cookies or its API.
 */
// readOnly (a conversation in the trash) leaves out the buttons that change the image allowlist.
export function MailBody({ conversationId, message, readOnly = false }: { conversationId: string; message: Message; readOnly?: boolean }) {
  const queryClient = useQueryClient()
  const frame = useRef<HTMLIFrameElement>(null)
  const [height, setHeight] = useState(160)
  const [src, setSrc] = useState(message.render_url)
  const [reloads, setReloads] = useState(0)
  const [shownOnce, setShownOnce] = useState(false)

  useEffect(() => {
    function onMessage(event: MessageEvent) {
      const next = frameHeight(event, frame.current?.contentWindow)
      if (next !== undefined) setHeight(next)
    }
    window.addEventListener('message', onMessage)
    return () => window.removeEventListener('message', onMessage)
  }, [])

  const allow = useMutation({
    mutationFn: (scope: ImageScope) =>
      api<{ render_url: string }>(
        'POST',
        `/conversations/${encodeURIComponent(conversationId)}/messages/${encodeURIComponent(message.id)}/allow-images?scope=${scope}`,
      ),
    onSuccess: (data, scope) => {
      setSrc(data.render_url)
      setReloads((n) => n + 1)
      if (scope === 'once') setShownOnce(true)
      else void queryClient.invalidateQueries({ queryKey: ['inbox', 'conversation', conversationId] })
    },
  })

  const frameSrc = reloads === 0 ? src : `${src}${src.includes('?') ? '&' : '?'}_=${reloads}`
  const blocked = message.blocked_images > 0 && !shownOnce

  return (
    <div className="flex w-full flex-col gap-3">
      <PhishingNotice warnings={message.phishing_warnings} />
      {blocked && (
        <div role="status" className="flex flex-wrap items-center gap-x-4 gap-y-2 rounded-md bg-subtle px-4 py-3 text-base text-ink">
          <span className="inline-flex items-center gap-2">
            <ImageOff size={16} aria-hidden className="shrink-0 text-muted" />
            Externe afbeeldingen zijn geblokkeerd.
          </span>
          <span className="flex flex-wrap gap-2">
            <Button size="sm" busy={allow.isPending && allow.variables === 'once'} disabled={allow.isPending} onClick={() => allow.mutate('once')}>
              Eenmalig tonen
            </Button>
            {!readOnly && (
              <>
                <Button size="sm" busy={allow.isPending && allow.variables === 'sender'} disabled={allow.isPending} onClick={() => allow.mutate('sender')}>
                  Altijd tonen van dit adres
                </Button>
                <Button size="sm" busy={allow.isPending && allow.variables === 'domain'} disabled={allow.isPending} onClick={() => allow.mutate('domain')}>
                  Altijd tonen van dit domein
                </Button>
              </>
            )}
          </span>
        </div>
      )}
      {allow.isError && <ErrorNotice>{errorMessage(allow.error)}</ErrorNotice>}
      <iframe
        ref={frame}
        title="Inhoud van het bericht"
        src={frameSrc}
        sandbox="allow-scripts allow-popups allow-popups-to-escape-sandbox"
        referrerPolicy="no-referrer"
        style={{ height }}
        className="block w-full rounded-md border border-line bg-paper"
      />
    </div>
  )
}
