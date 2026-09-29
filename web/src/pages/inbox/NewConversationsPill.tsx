import { ArrowUp } from 'lucide-react'

import { newConversationsLabel } from './liveList'

// The live region stays mounted so screen readers pick up later changes to its text.
export function NewConversationsPill({ count, announcement, onShow }: { count: number; announcement: string; onShow: () => void }) {
  return (
    <>
      <div role="status" aria-live="polite" className="sr-only">
        {announcement}
      </div>
      {count > 0 && (
        <div className="pointer-events-none sticky top-4 z-10 flex justify-center">
          <button
            type="button"
            onClick={onShow}
            className="btn btn-s btn-accent pointer-events-auto max-md:h-11"
          >
            <ArrowUp size={16} aria-hidden />
            {newConversationsLabel(count)}
          </button>
        </div>
      )}
    </>
  )
}
