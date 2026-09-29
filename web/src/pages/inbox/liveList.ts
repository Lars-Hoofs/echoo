import { useState } from 'react'

// New conversations sort to the top, so those are the leading ids the list has not seen yet.
// Ids that show up further down came from loading another page.
export function leadingNewIds(ids: readonly string[], known: ReadonlySet<string>): string[] {
  const out: string[] = []
  for (const id of ids) {
    if (known.has(id)) break
    out.push(id)
  }
  return out
}

export function newConversationsLabel(n: number): string {
  return n === 1 ? '1 nieuw gesprek' : `${n} nieuwe gesprekken`
}

interface LiveState {
  key: string
  loaded: boolean
  known: ReadonlySet<string>
  held: ReadonlySet<string>
  arrived: number
}

// Keeps conversations that arrive while the agent has scrolled down out of the list, so rows
// do not shift under the pointer. They are released when the agent returns to the top or asks for them.
export function useHeldNewConversations(ids: readonly string[], listKey: string, loaded: boolean, scrolled: boolean) {
  const [state, setState] = useState<LiveState>({ key: listKey, loaded: false, known: new Set(), held: new Set(), arrived: 0 })

  // State is adjusted during render (not in an effect) so a held row is never painted first.
  if (state.key !== listKey) {
    setState({ key: listKey, loaded: false, known: new Set(), held: new Set(), arrived: 0 })
  } else if (loaded && !state.loaded) {
    setState({ ...state, loaded: true, known: new Set(ids) })
  } else if (loaded) {
    const unhandled = ids.filter((id) => !state.known.has(id) && !state.held.has(id))
    const releasing = !scrolled && state.held.size > 0
    if (unhandled.length > 0 || releasing) {
      const leading = new Set(leadingNewIds(ids, state.known))
      const known = new Set(state.known)
      const held = new Set(state.held)
      let arrived = 0
      for (const id of unhandled) {
        if (scrolled && leading.has(id)) held.add(id)
        else known.add(id)
        if (!scrolled && leading.has(id)) arrived++
      }
      if (!scrolled) {
        held.forEach((id) => known.add(id))
        held.clear()
      }
      setState({ ...state, known, held, arrived: arrived > 0 ? arrived : state.arrived })
    }
  }

  const release = () => {
    setState((s) => ({ ...s, known: new Set([...s.known, ...s.held]), held: new Set() }))
  }
  const announcement = state.held.size > 0 ? newConversationsLabel(state.held.size) : state.arrived > 0 ? newConversationsLabel(state.arrived) : ''
  return { held: state.held, release, announcement }
}
