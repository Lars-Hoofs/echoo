import { FileText, TriangleAlert, X } from 'lucide-react-native'
import { useState } from 'react'
import { Linking, Modal, Pressable, StyleSheet, Text, View } from 'react-native'
import { SafeAreaView } from 'react-native-safe-area-context'
import { WebView } from 'react-native-webview'

import { server } from '../lib/api'
import { describeEvent, formatDateTime } from '../lib/text'
import type { ThreadEntry } from '../lib/thread'
import type { Attachment, Message } from '../lib/types'
import { Button, IconButton, T } from './components'
import { font, radius, size, space, useColors } from './theme'

export function ThreadEntryView({ entry }: { entry: ThreadEntry }) {
  if (entry.kind === 'event') return <EventLine text={describeEvent(entry.event)} at={entry.event.created_at} />
  const m = entry.message
  if (m.kind === 'note') return <Note message={m} />
  if (m.kind === 'system') return <EventLine text={m.body_text} at={m.received_at ?? ''} />
  return <Email message={m} />
}

function EventLine({ text, at }: { text: string; at: string }) {
  const c = useColors()
  return (
    <View style={styles.event}>
      <Text style={[styles.eventText, { color: c.ink2 }]}>
        {text}
        {at ? ` · ${formatDateTime(at)}` : ''}
      </Text>
    </View>
  )
}

function Note({ message: m }: { message: Message }) {
  const c = useColors()
  return (
    <View style={[styles.note, { backgroundColor: c.surface2, borderLeftColor: c.ink }]}>
      <View style={styles.noteHead}>
        <T variant="label">Interne notitie{m.author ? ` · ${m.author.name}` : ''}</T>
        <T variant="small" muted>
          {m.received_at ? formatDateTime(m.received_at) : ''}
        </T>
      </View>
      <T selectable>{m.body_text}</T>
    </View>
  )
}

const deliveryText: Partial<Record<NonNullable<Message['outbound_status']>, string>> = {
  queued: 'In de wachtrij',
  sending: 'Wordt verzonden',
  retry: 'Nog niet verzonden, Echoo probeert het opnieuw',
  failed: 'Niet verzonden',
  uncertain: 'Onzeker of dit is aangekomen',
  bounced: 'Niet bezorgd',
  cancelled: 'Geannuleerd',
}

function Email({ message: m }: { message: Message }) {
  const c = useColors()
  const out = m.direction === 'out'
  // Customer mail with only an HTML part has no useful text; show the rendered mail right away.
  const [html, setHtml] = useState(!out && m.has_html && !m.body_text.trim())
  const sender = out ? (m.author?.name ?? m.from.name) : m.from.name || m.from.address
  const time = m.received_at ?? m.sent_at
  const delivery = out && m.outbound_status ? deliveryText[m.outbound_status] : undefined
  const failed = m.outbound_status === 'failed' || m.outbound_status === 'bounced'

  return (
    <View style={[styles.email, out ? styles.outWrap : styles.inWrap]}>
      <T variant="label" numberOfLines={1} style={{ textAlign: out ? 'right' : 'left' }}>
        {sender}
      </T>
      <View
        style={[
          styles.bubble,
          out ? { backgroundColor: c.surface2, borderColor: c.surface2, borderBottomRightRadius: radius.s } : { backgroundColor: c.surface, borderColor: c.lineStrong, borderBottomLeftRadius: radius.s },
          html && styles.htmlBubble,
        ]}
      >
        {!out && m.phishing_warnings.length > 0 ? (
          <View style={[styles.warning, { backgroundColor: c.surface2 }]}>
            <TriangleAlert size={16} color={c.alertText} strokeWidth={1.5} />
            <T variant="label" style={{ flexShrink: 1 }}>
              Let op: deze mail lijkt op phishing. Klik niet zomaar op links.
            </T>
          </View>
        ) : null}
        {html ? <MailHtml message={m} /> : <T selectable>{m.body_text.trim() || '(geen tekst)'}</T>}
        {m.attachments.length > 0 ? <Attachments attachments={m.attachments} /> : null}
        <View style={styles.footer}>
          {!out && m.has_html ? (
            <Pressable accessibilityRole="button" onPress={() => setHtml(!html)} hitSlop={8}>
              <Text style={[styles.footerText, { color: c.ink, textDecorationLine: 'underline' }]}>{html ? 'Als tekst tonen' : 'Originele opmaak'}</Text>
            </Pressable>
          ) : null}
          {time ? <Text style={[styles.footerText, { color: c.ink2, marginLeft: 'auto' }]}>{formatDateTime(time)}</Text> : null}
        </View>
      </View>
      {delivery ? (
        <T variant="small" style={{ color: failed ? c.alertText : c.ink2, textAlign: 'right' }}>
          {delivery}
          {m.outbound_error ? `: ${m.outbound_error}` : ''}
        </T>
      ) : null}
    </View>
  )
}

// The render page reports its height to its parent frame; in the app it is the top window, so a
// bridge forwards that message to React Native. Native injection is not bound by the page's CSP.
const heightBridge = `window.addEventListener('message', function (e) {
  if (e.data && e.data.type === 'echoo:render-height') window.ReactNativeWebView.postMessage(String(e.data.height));
}); true;`

// The sanitized mail as the web app shows it: served by Echoo's render route, scripts off,
// links opened in the browser, images only when the agent allows them there.
function MailHtml({ message: m }: { message: Message }) {
  const [height, setHeight] = useState(200)
  const base = server()
  const uri = base + m.render_url
  return (
    <WebView
      source={{ uri }}
      style={{ height, backgroundColor: 'transparent' }}
      scrollEnabled={false}
      sharedCookiesEnabled
      originWhitelist={[base]}
      injectedJavaScriptBeforeContentLoaded={heightBridge}
      onMessage={(e) => {
        const h = Number(e.nativeEvent.data)
        if (Number.isFinite(h) && h > 0) setHeight(Math.min(h + 8, 20_000))
      }}
      onShouldStartLoadWithRequest={(req) => {
        if (req.url.startsWith(`${base}/render/`)) return true
        if (/^(https?|mailto|tel):/i.test(req.url)) void Linking.openURL(req.url)
        return false
      }}
      setSupportMultipleWindows={false}
      javaScriptCanOpenWindowsAutomatically={false}
      accessibilityLabel="Inhoud van het bericht"
    />
  )
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} kB`
  return `${(bytes / 1024 / 1024).toFixed(1).replace('.', ',')} MB`
}

function Attachments({ attachments }: { attachments: Attachment[] }) {
  const c = useColors()
  const [open, setOpen] = useState<Attachment | null>(null)
  return (
    <View style={styles.attachments}>
      {attachments.map((a) => {
        const blocked = a.scan_status === 'infected'
        return (
          <Pressable
            key={a.id}
            accessibilityRole="button"
            accessibilityLabel={`Bijlage ${a.filename}, ${formatSize(a.size)}`}
            disabled={blocked}
            onPress={() => setOpen(a)}
            style={[styles.attachment, { borderColor: c.lineStrong, opacity: blocked ? 0.5 : 1 }]}
          >
            <FileText size={16} color={c.ink2} strokeWidth={1.5} />
            <Text style={[styles.attachmentName, { color: c.ink }]} numberOfLines={1}>
              {a.filename || 'bijlage'}
            </Text>
            <Text style={[styles.footerText, { color: c.ink2 }]}>{blocked ? 'virus' : formatSize(a.size)}</Text>
          </Pressable>
        )
      })}
      {open ? <AttachmentViewer attachment={open} onClose={() => setOpen(null)} /> : null}
    </View>
  )
}

// The download needs the session cookie, which the in-app viewer shares; iOS and Android show
// PDFs and images themselves.
function AttachmentViewer({ attachment: a, onClose }: { attachment: Attachment; onClose: () => void }) {
  const c = useColors()
  const [confirmed, setConfirmed] = useState(!a.dangerous)
  return (
    <Modal animationType="slide" presentationStyle="pageSheet" onRequestClose={onClose} visible>
      <SafeAreaView style={{ flex: 1, backgroundColor: c.surface }}>
        <View style={styles.viewerHead}>
          <T variant="heading" numberOfLines={1} style={{ flex: 1 }}>
            {a.filename}
          </T>
          <IconButton label="Sluiten" onPress={onClose}>
            <X size={20} color={c.ink} strokeWidth={1.5} />
          </IconButton>
        </View>
        {confirmed ? (
          <WebView source={{ uri: server() + a.download_url }} sharedCookiesEnabled style={{ flex: 1 }} />
        ) : (
          <View style={{ padding: space[5], gap: space[4] }}>
            <T>Dit type bestand kan schadelijk zijn. Open het alleen als je de afzender vertrouwt.</T>
            <Button variant="danger" onPress={() => setConfirmed(true)}>
              Toch openen
            </Button>
          </View>
        )}
      </SafeAreaView>
    </Modal>
  )
}

const styles = StyleSheet.create({
  event: { alignItems: 'center', paddingHorizontal: space[5] },
  eventText: { fontFamily: font.regular, fontSize: size.xs, textAlign: 'center' },
  note: { borderLeftWidth: 2, borderTopRightRadius: radius.m, borderBottomRightRadius: radius.m, paddingHorizontal: space[4], paddingVertical: space[3], gap: space[2] },
  noteHead: { flexDirection: 'row', justifyContent: 'space-between', gap: space[2] },
  email: { gap: space[1], maxWidth: '88%' },
  inWrap: { alignSelf: 'flex-start' },
  outWrap: { alignSelf: 'flex-end' },
  bubble: { borderWidth: 1, borderRadius: radius.m, padding: space[4], gap: space[3] },
  htmlBubble: { width: '100%', minWidth: 300 },
  warning: { flexDirection: 'row', gap: space[2], alignItems: 'flex-start', borderRadius: radius.s, padding: space[3] },
  footer: { flexDirection: 'row', alignItems: 'center', gap: space[3] },
  footerText: { fontFamily: font.regular, fontSize: size.xs },
  attachments: { gap: space[2] },
  attachment: { flexDirection: 'row', alignItems: 'center', gap: space[2], borderWidth: 1, borderRadius: radius.pill, paddingHorizontal: space[3], height: 36 },
  attachmentName: { fontFamily: font.regular, fontSize: size.s, flexShrink: 1 },
  viewerHead: { flexDirection: 'row', alignItems: 'center', gap: space[3], padding: space[4] },
})
