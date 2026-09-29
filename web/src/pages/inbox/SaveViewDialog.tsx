import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { type SubmitEvent, useState } from 'react'

import { Dialog, DialogFooter } from '../../components/Dialog'
import { Button, ErrorNotice, Field, Input, Select } from '../../components/ui'
import { ApiError } from '../../lib/api'
import { errorMessage } from '../../lib/errors'
import { searchToSaved } from '../../lib/filters'
import { type InboxSearch } from '../../lib/inbox'
import { createSavedView, type SavedView, updateSavedView, type ViewScope } from '../../lib/savedViews'
import type { Ref } from '../../lib/inbox'

const nameErrors: Record<string, string> = {
  invalid: 'Vul een naam in (maximaal 60 tekens).',
  required: 'Vul een naam in.',
  taken: 'Er bestaat al een weergave met deze naam.',
}

// "personal", "everyone" or "team:<id>": one select for the scope and the team.
function parseChoice(choice: string): { scope: ViewScope; team_id?: string } {
  if (choice === 'personal' || choice === 'everyone') return { scope: choice }
  return { scope: 'team', team_id: choice.slice('team:'.length) }
}

// Creates a view from the current filters, or renames an existing one. Only admins can share.
export function SaveViewDialog({
  search,
  rename,
  canShare,
  teams,
  onClose,
}: {
  search: InboxSearch
  rename: SavedView | null
  canShare: boolean
  teams: Ref[]
  onClose: () => void
}) {
  const [name, setName] = useState(rename?.name ?? '')
  const [choice, setChoice] = useState('personal')
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const save = useMutation({
    mutationFn: () => (rename ? updateSavedView(rename.id, { name }) : createSavedView({ name, filters: searchToSaved(search), ...parseChoice(choice) })),
    onSuccess: async (view) => {
      await queryClient.invalidateQueries({ queryKey: ['inbox', 'saved-views'] })
      onClose()
      if (!rename) await navigate({ to: '/inbox/$view', params: { view: 'alle' }, search: { ...search, weergave: view.id } })
    },
  })
  const nameCode = save.error instanceof ApiError ? save.error.fields.name : undefined
  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    save.mutate()
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={rename ? 'Weergave hernoemen' : 'Opslaan als weergave'} size="sm">
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {save.isError && !nameCode && <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>}
        <Field label="Naam" error={nameCode ? (nameErrors[nameCode] ?? 'Ongeldige waarde.') : undefined}>
          {(p) => <Input {...p} autoFocus maxLength={60} value={name} onChange={(e) => setName(e.target.value)} />}
        </Field>
        {!rename && canShare && (
          <Field label="Opslaan in">
            {(p) => (
              <Select {...p} value={choice} onChange={(e) => setChoice(e.target.value)}>
                <option value="personal">Persoonlijk</option>
                <option value="everyone">Iedereen</option>
                {teams.map((t) => (
                  <option key={t.id} value={`team:${t.id}`}>
                    Team {t.name}
                  </option>
                ))}
              </Select>
            )}
          </Field>
        )}
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button type="submit" variant="primary" busy={save.isPending} disabled={!name.trim()}>
            Opslaan
          </Button>
        </DialogFooter>
      </form>
    </Dialog>
  )
}
