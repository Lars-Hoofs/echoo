import { useCallback, useEffect, useRef, useState } from 'react'

import { useToast } from '../../../components/Toast'
import { api, ApiError, apiUpload } from '../../../lib/api'
import type { Upload } from '../../../lib/composer'
import { errorMessage } from '../../../lib/errors'

export interface AttachmentItem {
  key: string
  name: string
  size: number
  progress: number
  upload?: Upload
  error?: string
}

// Tracks the files of one message: uploads them as soon as they are added, reports progress and
// removes an upload from the server when it is taken away again.
export function useAttachments(initial: Upload[]) {
  const toast = useToast()
  const [items, setItems] = useState<AttachmentItem[]>(() =>
    initial.map((u) => ({ key: u.id, name: u.filename, size: u.size, progress: 1, upload: u })),
  )
  const counter = useRef(0)
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])

  const patch = useCallback((key: string, change: Partial<AttachmentItem>) => {
    if (!mounted.current) return
    setItems((list) => list.map((i) => (i.key === key ? { ...i, ...change } : i)))
  }, [])

  const add = useCallback(
    (files: File[]) => {
      for (const file of files) {
        const key = `new-${counter.current++}`
        setItems((list) => [...list, { key, name: file.name, size: file.size, progress: 0 }])
        apiUpload<Upload>('/uploads', file, (progress) => {
          patch(key, { progress })
        }).then(
          (upload) => {
            patch(key, { upload, progress: 1 })
          },
          (err: unknown) => {
            patch(key, { error: errorMessage(err) })
          },
        )
      }
    },
    [patch],
  )

  const remove = useCallback(
    (key: string) => {
      const item = items.find((i) => i.key === key)
      setItems((list) => list.filter((i) => i.key !== key))
      if (!item?.upload) return
      api('DELETE', `/uploads/${item.upload.id}`).catch((err: unknown) => {
        if (!(err instanceof ApiError && err.status === 404)) toast(errorMessage(err), { tone: 'error' })
      })
    },
    [items, toast],
  )

  const clear = useCallback(() => {
    setItems([])
  }, [])

  const uploads = items.flatMap((i) => (i.upload ? [i.upload] : []))
  return {
    items,
    uploads,
    busy: items.some((i) => !i.upload && !i.error),
    failed: items.some((i) => i.error),
    add,
    remove,
    clear,
  }
}
