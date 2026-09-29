// Single-letter shortcuts must never fire while someone is typing or a menu or dialog is open.
export function isTypingTarget(t: EventTarget | null): boolean {
  if (!(t instanceof HTMLElement)) return false
  return t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName) || t.closest('[role=menu],[role=dialog]') !== null
}
