import { useQuery, useQueryClient } from '@tanstack/react-query'
import * as Crypto from 'expo-crypto'
import { ChevronDown, Zap } from 'lucide-react-native'
import { useState } from 'react'
import { Pressable, StyleSheet, Text, TextInput, View } from 'react-native'
import { useSafeAreaInsets } from 'react-native-safe-area-context'

import { api, errorMessage } from '../lib/api'
import { replyDefaultsQuery, templatesQuery } from '../lib/queries'
import { htmlToText, textToHtml } from '../lib/text'
import type { Message } from '../lib/types'
import { Segmented, Sheet, type SheetOption } from './components'
import { font, height, radius, size, space, useColors } from './theme'
import { useToast } from './toast'

type Mode = 'reply' | 'note'
type StatusAfter = 'waiting' | 'closed' | null

const sentText: Record<string, string> = { waiting: 'Verstuurd, gesprek staat op wachtend', closed: 'Verstuurd, gesprek gesloten' }

// Replying on a phone: plain text, sent as simple HTML the server sanitizes and signs. Canned
// responses come in as text with their variables filled in by the server.
export function Composer({ conversationId }: { conversationId: string }) {
  const c = useColors()
  const qc = useQueryClient()
  const toast = useToast()
  const insets = useSafeAreaInsets()
  const [open, setOpen] = useState(false)
  const [mode, setMode] = useState<Mode>('reply')
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [sheet, setSheet] = useState<'templates' | 'send' | null>(null)
  const defaults = useQuery({ ...replyDefaultsQuery(conversationId), enabled: open })
  const templates = useQuery({ ...templatesQuery, enabled: sheet === 'templates' })

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ['inbox', 'conversation', conversationId] })
    void qc.invalidateQueries({ queryKey: ['inbox', 'conversations'] })
    void qc.invalidateQueries({ queryKey: ['inbox', 'summary'] })
  }

  const send = async (statusAfter: StatusAfter) => {
    const body = text.trim()
    if (!body) return
    setBusy(true)
    try {
      if (mode === 'note') {
        await api('POST', `/conversations/${conversationId}/notes`, { html: textToHtml(body) })
        toast({ text: 'Notitie toegevoegd' })
      } else {
        const r = await api<{ message: Message; undo_until: string | null }>('POST', `/conversations/${conversationId}/replies`, {
          idempotency_key: Crypto.randomUUID(),
          html: textToHtml(body),
          status_after: statusAfter,
        })
        const undoMs = r.undo_until ? new Date(r.undo_until).getTime() - Date.now() : 0
        toast({
          text: statusAfter ? (sentText[statusAfter] ?? 'Verstuurd') : 'Verstuurd',
          ms: Math.max(undoMs, 4000),
          action:
            undoMs > 0
              ? {
                  label: 'Ongedaan maken',
                  onPress: () => {
                    api('POST', `/conversations/${conversationId}/replies/${r.message.id}/cancel`)
                      .then(() => {
                        setText(body)
                        setMode('reply')
                        setOpen(true)
                        toast({ text: 'Niet verstuurd, je tekst staat weer klaar' })
                        refresh()
                      })
                      .catch((err: unknown) => toast({ text: errorMessage(err), tone: 'alert' }))
                  },
                }
              : undefined,
        })
      }
      setText('')
      setOpen(false)
      refresh()
    } catch (err) {
      toast({ text: errorMessage(err), tone: 'alert' })
    } finally {
      setBusy(false)
    }
  }

  const insertTemplate = async (id: string) => {
    try {
      const r = await api<{ body_html: string }>('GET', `/templates/${id}/render?conversation_id=${encodeURIComponent(conversationId)}`)
      const t = htmlToText(r.body_html)
      setText((cur) => (cur.trim() ? `${cur.trimEnd()}\n\n${t}` : t))
    } catch (err) {
      toast({ text: errorMessage(err), tone: 'alert' })
    }
  }

  const bottom = Math.max(insets.bottom, space[2])

  if (!open) {
    return (
      <View style={[styles.bar, { paddingBottom: bottom, backgroundColor: c.surface, borderTopColor: c.line }]}>
        <Pressable
          accessibilityRole="button"
          onPress={() => setOpen(true)}
          style={[styles.placeholder, { borderColor: c.lineStrong, backgroundColor: c.surface }]}
        >
          <Text style={[styles.placeholderText, { color: c.ink2 }]}>Antwoord of notitie schrijven</Text>
        </Pressable>
      </View>
    )
  }

  const to = defaults.data?.to.map((a) => a.name || a.address).join(', ')
  let sheetOptions: SheetOption[] = []
  if (sheet === 'send') {
    sheetOptions = [
      { label: 'Versturen en op wachtend zetten', onPress: () => void send('waiting') },
      { label: 'Versturen en sluiten', onPress: () => void send('closed') },
    ]
  } else if (sheet === 'templates') {
    sheetOptions = (templates.data ?? []).map((t) => ({ label: t.name, onPress: () => void insertTemplate(t.id) }))
  }

  return (
    <View style={[styles.open, { paddingBottom: bottom, backgroundColor: c.surface, borderTopColor: c.line }]}>
      <View style={styles.row}>
        <View style={{ flex: 1 }}>
          <Segmented
            label="Soort bericht"
            value={mode}
            onChange={setMode}
            options={[
              { value: 'reply', label: 'Antwoorden' },
              { value: 'note', label: 'Notitie' },
            ]}
          />
        </View>
        <Pressable accessibilityRole="button" accessibilityLabel="Editor inklappen" onPress={() => setOpen(false)} hitSlop={8} style={styles.collapse}>
          <ChevronDown size={20} color={c.ink2} strokeWidth={1.5} />
        </Pressable>
      </View>
      {mode === 'reply' && to ? (
        <Text style={[styles.to, { color: c.ink2 }]} numberOfLines={1}>
          Aan: {to}
        </Text>
      ) : null}
      <TextInput
        value={text}
        onChangeText={setText}
        multiline
        autoFocus
        placeholder={mode === 'reply' ? 'Je antwoord aan de klant' : 'Alleen zichtbaar voor je team'}
        placeholderTextColor={c.ink3}
        accessibilityLabel={mode === 'reply' ? 'Antwoord' : 'Notitie'}
        style={[
          styles.input,
          { color: c.ink, borderColor: c.lineStrong, backgroundColor: mode === 'note' ? c.surface2 : c.surface },
        ]}
      />
      <View style={styles.row}>
        {mode === 'reply' ? (
          <Pressable
            accessibilityRole="button"
            accessibilityLabel="Standaardantwoord invoegen"
            onPress={() => setSheet('templates')}
            style={[styles.round, { borderColor: c.lineStrong }]}
          >
            <Zap size={18} color={c.ink} strokeWidth={1.5} />
          </Pressable>
        ) : null}
        <View style={{ flex: 1 }} />
        {mode === 'reply' ? (
          <View style={styles.split}>
            <Pressable
              accessibilityRole="button"
              accessibilityState={{ disabled: busy || !text.trim(), busy }}
              disabled={busy || !text.trim()}
              onPress={() => void send(null)}
              style={[styles.sendMain, { backgroundColor: c.accent, opacity: busy || !text.trim() ? 0.5 : 1 }]}
            >
              <Text style={[styles.sendText, { color: c.accentInk }]}>{busy ? 'Versturen…' : 'Versturen'}</Text>
            </Pressable>
            <Pressable
              accessibilityRole="button"
              accessibilityLabel="Meer opties voor versturen"
              disabled={busy || !text.trim()}
              onPress={() => setSheet('send')}
              style={[styles.sendMore, { backgroundColor: c.accent, borderLeftColor: c.accentInk, opacity: busy || !text.trim() ? 0.5 : 1 }]}
            >
              <ChevronDown size={18} color={c.accentInk} strokeWidth={1.5} />
            </Pressable>
          </View>
        ) : (
          <Pressable
            accessibilityRole="button"
            disabled={busy || !text.trim()}
            onPress={() => void send(null)}
            style={[styles.sendMain, styles.sendAlone, { backgroundColor: c.ink, opacity: busy || !text.trim() ? 0.5 : 1 }]}
          >
            <Text style={[styles.sendText, { color: c.surface }]}>Notitie opslaan</Text>
          </Pressable>
        )}
      </View>
      {sheet ? (
        <Sheet
          title={sheet === 'send' ? 'Versturen' : templates.isPending ? 'Standaardantwoorden laden…' : sheetOptions.length ? 'Standaardantwoord' : 'Nog geen standaardantwoorden'}
          options={sheetOptions}
          onClose={() => setSheet(null)}
        />
      ) : null}
    </View>
  )
}

const styles = StyleSheet.create({
  bar: { paddingHorizontal: space[4], paddingTop: space[3], borderTopWidth: StyleSheet.hairlineWidth },
  placeholder: { height: height.m, borderWidth: 1, borderRadius: radius.pill, justifyContent: 'center', paddingHorizontal: space[4] },
  placeholderText: { fontFamily: font.regular, fontSize: size.m },
  open: { paddingHorizontal: space[4], paddingTop: space[3], gap: space[3], borderTopWidth: StyleSheet.hairlineWidth },
  row: { flexDirection: 'row', alignItems: 'center', gap: space[2] },
  collapse: { width: 36, height: 36, alignItems: 'center', justifyContent: 'center' },
  to: { fontFamily: font.regular, fontSize: size.s },
  input: {
    fontFamily: font.regular,
    fontSize: size.m,
    lineHeight: 22,
    minHeight: 96,
    maxHeight: 240,
    borderWidth: 1,
    borderRadius: radius.m,
    paddingHorizontal: space[4],
    paddingTop: space[3],
    paddingBottom: space[3],
    textAlignVertical: 'top',
  },
  round: { width: height.m, height: height.m, borderRadius: radius.pill, borderWidth: 1, alignItems: 'center', justifyContent: 'center' },
  split: { flexDirection: 'row' },
  sendMain: { height: height.m, paddingHorizontal: space[5], borderTopLeftRadius: radius.pill, borderBottomLeftRadius: radius.pill, justifyContent: 'center' },
  sendAlone: { borderRadius: radius.pill },
  sendMore: { height: height.m, width: 44, borderTopRightRadius: radius.pill, borderBottomRightRadius: radius.pill, justifyContent: 'center', alignItems: 'center', borderLeftWidth: StyleSheet.hairlineWidth },
  sendText: { fontFamily: font.regular, fontSize: size.m },
})
