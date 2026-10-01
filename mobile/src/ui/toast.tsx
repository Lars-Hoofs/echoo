import { createContext, type ReactNode, useCallback, useContext, useEffect, useRef, useState } from 'react'
import { AccessibilityInfo, Pressable, StyleSheet, Text, View } from 'react-native'
import { useSafeAreaInsets } from 'react-native-safe-area-context'

import { font, radius, size, space, useColors } from './theme'

interface ToastInput {
  text: string
  tone?: 'neutral' | 'alert'
  action?: { label: string; onPress: () => void }
  // How long it stays; an undo stays as long as the undo window.
  ms?: number
}

const ToastContext = createContext<(t: ToastInput) => void>(() => undefined)

export function useToast() {
  return useContext(ToastContext)
}

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toast, setToast] = useState<(ToastInput & { key: number }) | null>(null)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const c = useColors()
  const insets = useSafeAreaInsets()

  const show = useCallback((t: ToastInput) => {
    if (timer.current) clearTimeout(timer.current)
    setToast({ ...t, key: Date.now() })
    AccessibilityInfo.announceForAccessibility(t.text)
    timer.current = setTimeout(() => setToast(null), t.ms ?? 4000)
  }, [])

  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current)
  }, [])

  return (
    <ToastContext.Provider value={show}>
      {children}
      {toast ? (
        <View pointerEvents="box-none" style={[styles.wrap, { bottom: insets.bottom + 96 }]}>
          <View style={[styles.toast, { backgroundColor: c.ink }]} accessibilityLiveRegion="polite">
            <Text style={[styles.text, { color: toast.tone === 'alert' ? c.alert : c.surface }]}>{toast.text}</Text>
            {toast.action ? (
              <Pressable
                accessibilityRole="button"
                onPress={() => {
                  setToast(null)
                  toast.action?.onPress()
                }}
                hitSlop={8}
              >
                <Text style={[styles.text, styles.action, { color: c.surface }]}>{toast.action.label}</Text>
              </Pressable>
            ) : null}
          </View>
        </View>
      ) : null}
    </ToastContext.Provider>
  )
}

const styles = StyleSheet.create({
  wrap: { position: 'absolute', left: space[4], right: space[4], alignItems: 'center' },
  toast: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: space[4],
    borderRadius: radius.pill,
    paddingHorizontal: space[5],
    minHeight: 48,
    paddingVertical: space[3],
    maxWidth: 480,
  },
  text: { fontFamily: font.regular, fontSize: size.s, flexShrink: 1 },
  action: { fontFamily: font.medium, textDecorationLine: 'underline' },
})
