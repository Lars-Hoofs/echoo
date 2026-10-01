import { type ComponentProps, type ReactNode, useState } from 'react'
import {
  ActivityIndicator,
  Modal,
  Pressable,
  type StyleProp,
  StyleSheet,
  Text,
  TextInput,
  type TextStyle,
  View,
  type ViewStyle,
} from 'react-native'
import { useSafeAreaInsets } from 'react-native-safe-area-context'

import { initials } from '../lib/text'
import { type Colors, font, height, radius, size, space, useColors } from './theme'

// ron's scale on a phone: light display numerals and titles, regular body, medium only for
// emphasis. Hierarchy comes from size, never from bold.
type Variant = 'display' | 'title' | 'heading' | 'body' | 'label' | 'small'
const variants: Record<Variant, TextStyle> = {
  display: { fontFamily: font.light, fontSize: size.xxl, lineHeight: 44, letterSpacing: -0.8 },
  title: { fontFamily: font.light, fontSize: size.xl, lineHeight: 32, letterSpacing: -0.4 },
  heading: { fontFamily: font.regular, fontSize: size.l, lineHeight: 26, letterSpacing: -0.2 },
  body: { fontFamily: font.regular, fontSize: size.m, lineHeight: 24, letterSpacing: -0.16 },
  label: { fontFamily: font.regular, fontSize: size.s, lineHeight: 20 },
  small: { fontFamily: font.regular, fontSize: size.xs, lineHeight: 16 },
}

export function T({
  variant = 'body',
  muted,
  medium,
  style,
  ...props
}: ComponentProps<typeof Text> & { variant?: Variant; muted?: boolean; medium?: boolean }) {
  const c = useColors()
  return (
    <Text
      {...props}
      style={[variants[variant], { color: muted ? c.ink2 : c.ink }, medium && { fontFamily: font.medium }, style]}
      maxFontSizeMultiplier={1.6}
    />
  )
}

type ButtonVariant = 'primary' | 'accent' | 'secondary' | 'ghost' | 'danger'

function buttonColors(c: Colors, v: ButtonVariant): { bg: string; fg: string; border: string } {
  switch (v) {
    case 'primary':
      return { bg: c.ink, fg: c.surface, border: c.ink }
    case 'accent':
      return { bg: c.accent, fg: c.accentInk, border: c.accent }
    case 'danger':
      return { bg: c.alert, fg: c.alertInk, border: c.alert }
    case 'ghost':
      return { bg: 'transparent', fg: c.ink, border: 'transparent' }
    case 'secondary':
      return { bg: c.surface, fg: c.ink, border: c.lineStrong }
  }
}

export function Button({
  children,
  onPress,
  variant = 'secondary',
  large,
  busy,
  disabled,
  icon,
  style,
  accessibilityLabel,
}: {
  children: ReactNode
  onPress: () => void
  variant?: ButtonVariant
  large?: boolean
  busy?: boolean
  disabled?: boolean
  icon?: ReactNode
  style?: StyleProp<ViewStyle>
  accessibilityLabel?: string
}) {
  const c = useColors()
  const col = buttonColors(c, variant)
  const off = disabled || busy
  return (
    <Pressable
      accessibilityRole="button"
      accessibilityLabel={accessibilityLabel}
      accessibilityState={{ disabled: off, busy }}
      disabled={off}
      onPress={onPress}
      style={({ pressed }) => [
        styles.button,
        { height: large ? height.l : height.m, backgroundColor: col.bg, borderColor: col.border, opacity: off ? 0.5 : pressed ? 0.8 : 1 },
        pressed && styles.pressed,
        style,
      ]}
    >
      {busy ? <ActivityIndicator color={col.fg} /> : icon}
      <Text style={[variants.body, { color: col.fg }]} numberOfLines={1}>
        {children}
      </Text>
    </Pressable>
  )
}

export function IconButton({ label, onPress, children }: { label: string; onPress: () => void; children: ReactNode }) {
  const c = useColors()
  return (
    <Pressable
      accessibilityRole="button"
      accessibilityLabel={label}
      hitSlop={4}
      onPress={onPress}
      style={({ pressed }) => [styles.iconButton, { borderColor: c.lineStrong, backgroundColor: pressed ? c.surface2 : c.surface }, pressed && styles.pressed]}
    >
      {children}
    </Pressable>
  )
}

export function Card({ children, style, flush }: { children: ReactNode; style?: StyleProp<ViewStyle>; flush?: boolean }) {
  const c = useColors()
  return <View style={[styles.card, { backgroundColor: c.surface, borderColor: c.line, padding: flush ? 0 : space[4] }, style]}>{children}</View>
}

export function Tag({ children, tone = 'neutral' }: { children: ReactNode; tone?: 'neutral' | 'accent' | 'alert' }) {
  const c = useColors()
  const bg = tone === 'accent' ? c.accent : tone === 'alert' ? c.alert : c.surface2
  const fg = tone === 'accent' ? c.accentInk : tone === 'alert' ? c.alertInk : c.ink2
  return (
    <View style={[styles.tag, { backgroundColor: bg }]}>
      <Text style={[variants.small, { color: fg }]} numberOfLines={1}>
        {children}
      </Text>
    </View>
  )
}

export function Avatar({ name, size: s = 40 }: { name: string; size?: number }) {
  const c = useColors()
  return (
    <View style={{ width: s, height: s, borderRadius: s / 2, backgroundColor: c.surface2, alignItems: 'center', justifyContent: 'center' }}>
      <Text style={[variants.label, { color: c.ink2, fontSize: s * 0.36 }]}>{initials(name)}</Text>
    </View>
  )
}

// ron's tabs: a grey pill track with the chosen option as a solid ink pill.
export function Segmented<V extends string>({
  options,
  value,
  onChange,
  label,
}: {
  options: { value: V; label: string; count?: number }[]
  value: V
  onChange: (v: V) => void
  label: string
}) {
  const c = useColors()
  return (
    <View accessibilityRole="tablist" accessibilityLabel={label} style={[styles.segmented, { backgroundColor: c.surface2 }]}>
      {options.map((o) => {
        const active = o.value === value
        return (
          <Pressable
            key={o.value}
            accessibilityRole="tab"
            accessibilityState={{ selected: active }}
            onPress={() => onChange(o.value)}
            style={[styles.segment, active && { backgroundColor: c.ink }]}
          >
            <Text style={[variants.label, { color: active ? c.surface : c.ink2 }]} numberOfLines={1}>
              {o.label}
              {o.count ? `  ${o.count}` : ''}
            </Text>
          </Pressable>
        )
      })}
    </View>
  )
}

export function Field({
  label,
  help,
  error,
  multiline,
  ...input
}: ComponentProps<typeof TextInput> & { label: string; help?: string; error?: string | null }) {
  const c = useColors()
  const [focused, setFocused] = useState(false)
  return (
    <View style={{ gap: space[2] }}>
      <Text style={[variants.label, { color: c.ink2 }]}>{label}</Text>
      <TextInput
        {...input}
        multiline={multiline}
        accessibilityLabel={label}
        placeholderTextColor={c.ink3}
        onFocus={(e) => {
          setFocused(true)
          input.onFocus?.(e)
        }}
        onBlur={(e) => {
          setFocused(false)
          input.onBlur?.(e)
        }}
        style={[
          variants.body,
          styles.input,
          multiline && styles.multiline,
          { color: c.ink, backgroundColor: c.surface, borderColor: error ? c.alert : focused ? c.ink : c.lineStrong },
        ]}
      />
      {error ? <Text style={[variants.label, { color: c.alertText }]}>{error}</Text> : help ? <Text style={[variants.label, { color: c.ink2 }]}>{help}</Text> : null}
    </View>
  )
}

export function Notice({ children, tone = 'neutral' }: { children: ReactNode; tone?: 'neutral' | 'alert' }) {
  const c = useColors()
  return (
    <View style={[styles.notice, { backgroundColor: c.surface2, borderColor: tone === 'alert' ? c.alert : c.line }]}>
      <Text style={[variants.label, { color: tone === 'alert' ? c.alertText : c.ink }]}>{children}</Text>
    </View>
  )
}

export function EmptyState({ icon, title, text }: { icon: ReactNode; title: string; text?: string }) {
  const c = useColors()
  return (
    <View style={styles.empty}>
      <View style={[styles.emptyIcon, { backgroundColor: c.surface2 }]}>{icon}</View>
      <T variant="heading" style={{ textAlign: 'center' }}>
        {title}
      </T>
      {text ? (
        <T muted style={{ textAlign: 'center' }}>
          {text}
        </T>
      ) : null}
    </View>
  )
}

export interface SheetOption {
  label: string
  onPress: () => void
  destructive?: boolean
  selected?: boolean
}

// A bottom sheet with a list of choices, for actions that do not fit the header.
export function Sheet({ title, options, onClose }: { title: string; options: SheetOption[]; onClose: () => void }) {
  const c = useColors()
  const insets = useSafeAreaInsets()
  return (
    <Modal transparent animationType="slide" onRequestClose={onClose} visible>
      <Pressable style={[StyleSheet.absoluteFill, { backgroundColor: c.scrim }]} onPress={onClose} accessibilityLabel="Sluiten" />
      <View style={[styles.sheet, { backgroundColor: c.surface, paddingBottom: insets.bottom + space[4] }]}>
        <T variant="label" muted style={{ paddingHorizontal: space[5], paddingBottom: space[2] }}>
          {title}
        </T>
        {options.map((o) => (
          <Pressable
            key={o.label}
            accessibilityRole="button"
            accessibilityState={{ selected: o.selected }}
            onPress={() => {
              onClose()
              o.onPress()
            }}
            style={({ pressed }) => [styles.sheetRow, pressed && { backgroundColor: c.surface2 }]}
          >
            <Text style={[variants.body, { color: o.destructive ? c.alertText : c.ink }]}>{o.label}</Text>
            {o.selected ? <View style={[styles.dot, { backgroundColor: c.ink }]} /> : null}
          </Pressable>
        ))}
      </View>
    </Modal>
  )
}

const styles = StyleSheet.create({
  button: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    gap: space[2],
    paddingHorizontal: space[5],
    borderRadius: radius.pill,
    borderWidth: 1,
  },
  pressed: { transform: [{ scale: 0.98 }] },
  iconButton: { width: height.m, height: height.m, borderRadius: radius.pill, borderWidth: 1, alignItems: 'center', justifyContent: 'center' },
  card: { borderRadius: radius.m, borderWidth: 1, overflow: 'hidden' },
  tag: { borderRadius: radius.pill, paddingHorizontal: space[3], paddingVertical: 2, alignSelf: 'flex-start' },
  segmented: { flexDirection: 'row', borderRadius: radius.pill, padding: space[1], gap: space[1] },
  segment: { flex: 1, height: 36, borderRadius: radius.pill, alignItems: 'center', justifyContent: 'center', paddingHorizontal: space[2] },
  input: { minHeight: height.m, borderWidth: 1, borderRadius: radius.pill, paddingHorizontal: space[4], paddingVertical: space[2] },
  multiline: { borderRadius: radius.m, minHeight: 120, textAlignVertical: 'top', paddingTop: space[3] },
  notice: { borderRadius: radius.s, borderWidth: 1, paddingHorizontal: space[4], paddingVertical: space[3] },
  empty: { alignItems: 'center', gap: space[3], paddingHorizontal: space[6], paddingVertical: space[7] },
  emptyIcon: { width: 56, height: 56, borderRadius: 28, alignItems: 'center', justifyContent: 'center' },
  sheet: { position: 'absolute', left: 0, right: 0, bottom: 0, borderTopLeftRadius: radius.m, borderTopRightRadius: radius.m, paddingTop: space[5] },
  sheetRow: { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', minHeight: 52, paddingHorizontal: space[5] },
  dot: { width: 8, height: 8, borderRadius: 4 },
})
