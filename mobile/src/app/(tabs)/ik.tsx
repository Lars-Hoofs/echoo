import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { Linking, Platform, ScrollView, StyleSheet, Switch, View } from 'react-native'
import { SafeAreaView } from 'react-native-safe-area-context'

import { api, errorMessage } from '../../lib/api'
import { enablePush } from '../../lib/push'
import { notificationSettingsQuery, pushConfigQuery, pushDevicesQuery } from '../../lib/queries'
import { useMe, useSession } from '../../lib/session'
import type { PushPrefs, User } from '../../lib/types'
import { Avatar, Button, Card, Notice, Segmented, T } from '../../ui/components'
import { radius, space, useColors } from '../../ui/theme'
import { useToast } from '../../ui/toast'

export default function MeScreen() {
  const c = useColors()
  const me = useMe()
  const { state, logout, refresh } = useSession()
  const server = 'server' in state ? state.server : ''

  return (
    <SafeAreaView edges={['top']} style={{ flex: 1, backgroundColor: c.bg }}>
      <ScrollView contentContainerStyle={styles.scroll}>
        <T variant="display" style={{ paddingHorizontal: space[2] }}>
          Ik
        </T>
        <Card style={styles.profile}>
          <Avatar name={me.user.name} size={48} />
          <View style={{ flex: 1 }}>
            <T variant="heading">{me.user.name}</T>
            <T variant="label" muted numberOfLines={1}>
              {me.user.email}
            </T>
          </View>
        </Card>
        <Availability current={me.user.availability} onSaved={() => void refresh()} />
        <PushCard />
        <Card style={{ gap: space[3] }}>
          <T variant="label" muted>
            Verbonden met
          </T>
          <T>{server.replace('https://', '')}</T>
          <T variant="label" muted>
            Instellingen voor je werkruimte, rapportages en campagnes staan in de web-app.
          </T>
          <Button onPress={() => void Linking.openURL(`${server}/inbox/alle`)}>Openen in de browser</Button>
        </Card>
        <Button variant="ghost" onPress={() => void logout()}>
          Uitloggen
        </Button>
      </ScrollView>
    </SafeAreaView>
  )
}

const availabilityLabel: Record<User['availability'], string> = { online: 'Online', busy: 'Bezet', offline: 'Offline' }

function Availability({ current, onSaved }: { current: User['availability']; onSaved: () => void }) {
  const toast = useToast()
  const [value, setValue] = useState(current)
  return (
    <Card style={{ gap: space[3] }}>
      <T variant="heading">Beschikbaarheid</T>
      <T variant="label" muted>
        Alleen wie online is, krijgt automatisch nieuwe gesprekken toegewezen.
      </T>
      <Segmented
        label="Beschikbaarheid"
        value={value}
        onChange={(v) => {
          setValue(v)
          api('PUT', '/me/availability', { availability: v })
            .then(onSaved)
            .catch((err: unknown) => {
              setValue(current)
              toast({ text: errorMessage(err), tone: 'alert' })
            })
        }}
        options={(['online', 'busy', 'offline'] as const).map((a) => ({ value: a, label: availabilityLabel[a] }))}
      />
    </Card>
  )
}

const kinds: { key: keyof PushPrefs; label: string }[] = [
  { key: 'replies', label: 'Klant antwoordt in mijn gesprek' },
  { key: 'assignments', label: 'Gesprek aan mij toegewezen' },
  { key: 'mentions', label: 'Iemand noemt mij' },
  { key: 'sla', label: 'SLA-termijn in gevaar' },
]

function PushCard() {
  const c = useColors()
  const qc = useQueryClient()
  const toast = useToast()
  const devices = useQuery(pushDevicesQuery)
  const settings = useQuery(notificationSettingsQuery)
  const config = useQuery(pushConfigQuery)
  // The server sends to Apple or Google only when its administrator set up the keys.
  const available = config.data ? (Platform.OS === 'ios' ? config.data.apns : config.data.fcm) : true
  const current = devices.data?.find((d) => d.current)
  const [denied, setDenied] = useState(false)

  const enable = useMutation({
    mutationFn: enablePush,
    onSuccess: (device) => {
      setDenied(device === null)
      if (device) toast({ text: 'Pushmeldingen staan aan op deze telefoon' })
      void qc.invalidateQueries({ queryKey: ['push'] })
    },
    onError: (err) => toast({ text: errorMessage(err), tone: 'alert' }),
  })
  const disable = useMutation({
    mutationFn: (id: string) => api('DELETE', `/push/devices/${id}`),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['push'] }),
    onError: (err) => toast({ text: errorMessage(err), tone: 'alert' }),
  })
  const test = useMutation({
    mutationFn: () => api<{ results: { ok: boolean }[] }>('POST', '/push/test'),
    onSuccess: (r) => {
      const ok = r.results.filter((x) => x.ok).length
      toast(ok === r.results.length ? { text: 'Testmelding verstuurd' } : { text: `${ok} van ${r.results.length} apparaten bereikt`, tone: 'alert' })
    },
    onError: (err) => toast({ text: errorMessage(err), tone: 'alert' }),
  })
  const save = useMutation({
    mutationFn: (push: PushPrefs) => api<{ push: PushPrefs }>('PUT', '/me/notification-settings', { push }),
    onSuccess: (r) => qc.setQueryData(notificationSettingsQuery.queryKey, (old) => (old ? { ...old, push: r.push } : old)),
    onError: (err) => toast({ text: errorMessage(err), tone: 'alert' }),
  })

  const prefs = settings.data?.push
  return (
    <Card style={{ gap: space[4] }}>
      <View style={styles.switchRow}>
        <View style={{ flex: 1, gap: 2 }}>
          <T variant="heading">Pushmeldingen</T>
          <T variant="label" muted>
            {current ? 'Deze telefoon krijgt meldingen, ook als Echoo dicht is.' : 'Zet ze aan om bereikbaar te zijn als het ertoe doet.'}
          </T>
        </View>
        <Switch
          value={Boolean(current)}
          disabled={!available || devices.isPending || enable.isPending || disable.isPending}
          onValueChange={(on) => (on ? enable.mutate() : current && disable.mutate(current.id))}
          trackColor={{ true: c.ink, false: c.surface2 }}
          thumbColor={c.surface}
          ios_backgroundColor={c.surface2}
          accessibilityLabel="Pushmeldingen op deze telefoon"
        />
      </View>
      {!available ? (
        <Notice>
          Pushmeldingen naar de {Platform.OS === 'ios' ? 'iPhone' : 'Android'}-app zijn op deze Echoo nog niet ingesteld. Vraag je beheerder om ze aan te zetten.
        </Notice>
      ) : null}
      {denied ? (
        <Notice>Meldingen staan uit voor Echoo. Zet ze aan in de instellingen van je telefoon.</Notice>
      ) : null}
      {denied ? <Button onPress={() => void Linking.openSettings()}>Instellingen openen</Button> : null}
      {prefs ? (
        <View style={[styles.kinds, { borderColor: c.line }]}>
          {kinds.map((k, i) => (
            <View key={k.key} style={[styles.switchRow, styles.kind, i > 0 && { borderTopColor: c.line, borderTopWidth: StyleSheet.hairlineWidth }]}>
              <T style={{ flex: 1 }}>{k.label}</T>
              <Switch
                value={prefs[k.key]}
                disabled={save.isPending}
                onValueChange={(on) => save.mutate({ ...prefs, [k.key]: on })}
                trackColor={{ true: c.ink, false: c.surface2 }}
                thumbColor={c.surface}
                ios_backgroundColor={c.surface2}
                accessibilityLabel={k.label}
              />
            </View>
          ))}
        </View>
      ) : null}
      {current ? (
        <Button busy={test.isPending} onPress={() => test.mutate()}>
          Testmelding sturen
        </Button>
      ) : null}
    </Card>
  )
}

const styles = StyleSheet.create({
  scroll: { padding: space[2], paddingTop: space[5], gap: space[3] },
  profile: { flexDirection: 'row', alignItems: 'center', gap: space[3] },
  switchRow: { flexDirection: 'row', alignItems: 'center', gap: space[3] },
  kinds: { borderWidth: 1, borderRadius: radius.m },
  kind: { paddingHorizontal: space[4], paddingVertical: space[3] },
})
