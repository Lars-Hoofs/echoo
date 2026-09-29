import { useCallback, useState } from 'react'

// localStorage can throw (blocked site data, private windows); the UI must work without it.
export function readStored(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

export function writeStored(key: string, value: string) {
  try {
    localStorage.setItem(key, value)
  } catch {
    // Not persisting a UI preference is harmless.
  }
}

export function usePersistedFlag(key: string, initial: boolean): [boolean, () => void] {
  const [value, setValue] = useState(() => {
    const stored = readStored(key)
    return stored === null ? initial : stored === '1'
  })
  const toggle = useCallback(() => {
    writeStored(key, value ? '0' : '1')
    setValue(!value)
  }, [key, value])
  return [value, toggle]
}
