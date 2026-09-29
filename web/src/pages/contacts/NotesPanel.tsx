import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Pencil, Trash2 } from 'lucide-react'
import { type SubmitEvent, useState } from 'react'

import { Avatar } from '../../components/Avatar'
import { Button, Card, ErrorNotice, Field, Skeleton, Textarea } from '../../components/ui'
import { api } from '../../lib/api'
import type { Note } from '../../lib/contacts'
import { errorMessage, fieldError } from '../../lib/errors'
import { formatDateTime } from '../../lib/format'
import { hasPermission, meQuery } from '../../lib/session'

export type NoteScope = 'contacts' | 'organizations'

export const notesKey = (scope: NoteScope, id: string, limit?: number) => ['notes', scope, id, limit ?? 'all']

export function NotesPanel({ scope, id }: { scope: NoteScope; id: string }) {
  const me = useQuery(meQuery)
  const notes = useQuery({
    queryKey: notesKey(scope, id),
    queryFn: async () => (await api<{ notes: Note[] }>('GET', `/${scope}/${id}/notes`)).notes,
  })
  const queryClient = useQueryClient()
  const refresh = () => queryClient.invalidateQueries({ queryKey: ['notes', scope, id] })
  const [body, setBody] = useState('')
  const add = useMutation({
    mutationFn: () => api('POST', `/${scope}/${id}/notes`, { body }),
    onSuccess: async () => {
      setBody('')
      await refresh()
      await queryClient.invalidateQueries({ queryKey: ['timeline', id] })
    },
  })
  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    add.mutate()
  }
  return (
    <div className="flex flex-col gap-4">
      {hasPermission(me.data, 'contacts.write') && (
        <form onSubmit={submit} className="flex flex-col gap-2" noValidate>
          <Field label="Nieuwe notitie" error={fieldError(add.error, 'body')}>
            {(p) => <Textarea {...p} maxLength={10000} value={body} onChange={(e) => setBody(e.target.value)} />}
          </Field>
          {add.isError && !fieldError(add.error, 'body') && <ErrorNotice>{errorMessage(add.error)}</ErrorNotice>}
          <div>
            <Button type="submit" variant="primary" busy={add.isPending} disabled={!body.trim()}>
              Notitie toevoegen
            </Button>
          </div>
        </form>
      )}
      {notes.isPending ? (
        <Skeleton className="h-24" />
      ) : notes.isError ? (
        <ErrorNotice>{errorMessage(notes.error)}</ErrorNotice>
      ) : notes.data.length === 0 ? (
        <p className="text-base text-muted">Nog geen notities.</p>
      ) : (
        <ul className="flex flex-col gap-3">
          {notes.data.map((n) => (
            <NoteItem key={n.id} note={n} scope={scope} id={id} onChanged={refresh} />
          ))}
        </ul>
      )}
    </div>
  )
}

function NoteItem({ note, scope, id, onChanged }: { note: Note; scope: NoteScope; id: string; onChanged: () => Promise<void> }) {
  const [editing, setEditing] = useState(false)
  const [body, setBody] = useState(note.body)
  const save = useMutation({
    mutationFn: () => api('PATCH', `/${scope}/${id}/notes/${note.id}`, { body }),
    onSuccess: async () => {
      setEditing(false)
      await onChanged()
    },
  })
  const remove = useMutation({
    mutationFn: () => api('DELETE', `/${scope}/${id}/notes/${note.id}`),
    onSuccess: onChanged,
  })
  const author = note.author?.name ?? 'Onbekende gebruiker'
  return (
    <li>
      <Card>
        <div className="flex items-start gap-3">
          <Avatar name={author} />
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-baseline gap-x-2">
              <span className="text-base text-ink">{author}</span>
              <span className="t-label">
                {formatDateTime(note.created_at)}
                {note.updated_at !== note.created_at && ' (bewerkt)'}
              </span>
            </div>
            {editing ? (
              <div className="mt-2 flex flex-col gap-2">
                <Textarea aria-label="Notitie bewerken" maxLength={10000} value={body} onChange={(e) => setBody(e.target.value)} />
                {(save.isError || remove.isError) && <ErrorNotice>{errorMessage(save.error ?? remove.error)}</ErrorNotice>}
                <div className="flex gap-2">
                  <Button variant="primary" size="sm" busy={save.isPending} disabled={!body.trim()} onClick={() => save.mutate()}>
                    Opslaan
                  </Button>
                  <Button
                    size="sm"
                    onClick={() => {
                      setEditing(false)
                      setBody(note.body)
                    }}
                  >
                    Annuleren
                  </Button>
                </div>
              </div>
            ) : (
              <p className="mt-1 text-base wrap-anywhere whitespace-pre-wrap text-ink">{note.body}</p>
            )}
            {remove.isError && !editing && <ErrorNotice>{errorMessage(remove.error)}</ErrorNotice>}
          </div>
          {note.can_edit && !editing && (
            <div className="flex shrink-0 gap-1">
              <Button size="sm" variant="ghost" aria-label="Notitie bewerken" onClick={() => setEditing(true)}>
                <Pencil size={14} aria-hidden />
                Bewerken
              </Button>
              <Button size="sm" variant="ghost" aria-label="Notitie verwijderen" busy={remove.isPending} onClick={() => remove.mutate()}>
                <Trash2 size={14} aria-hidden />
                Verwijderen
              </Button>
            </div>
          )}
        </div>
      </Card>
    </li>
  )
}
