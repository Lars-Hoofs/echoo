import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, X } from 'lucide-react'
import { type SubmitEvent, useState } from 'react'

import { Dialog, DialogFooter } from '../../components/Dialog'
import { Button, ErrorNotice, Field, IconButton, Input } from '../../components/ui'
import { api, ApiError } from '../../lib/api'
import { attributeDefsQuery, attributeDrafts, attributesToPatch, type OrganizationFull } from '../../lib/contacts'
import { errorMessage, fieldError } from '../../lib/errors'
import { AttributeFields } from './fields'

export function OrganizationDialog({
  organization,
  onClose,
  onSaved,
}: {
  organization?: OrganizationFull
  onClose: () => void
  onSaved?: (id: string) => void
}) {
  const defs = useQuery(attributeDefsQuery('organization'))
  const [name, setName] = useState(organization?.name ?? '')
  const [domains, setDomains] = useState<string[]>(organization?.domains ?? [])
  const [drafts, setDrafts] = useState<Record<string, string>>({})
  const [seeded, setSeeded] = useState(false)
  if (defs.data && !seeded) {
    setSeeded(true)
    setDrafts(attributeDrafts(defs.data, organization?.custom_attributes ?? {}))
  }
  const queryClient = useQueryClient()
  const save = useMutation({
    mutationFn: () => {
      const body = {
        name,
        domains: domains.map((d) => d.trim()).filter(Boolean),
        custom_attributes: attributesToPatch(defs.data ?? [], drafts, organization?.custom_attributes ?? {}),
      }
      return organization
        ? api<{ organization: OrganizationFull }>('PATCH', `/organizations/${organization.id}`, body)
        : api<{ organization: OrganizationFull }>('POST', '/organizations', body)
    },
    onSuccess: async (r) => {
      await queryClient.invalidateQueries({ queryKey: ['organizations'] })
      await queryClient.invalidateQueries({ queryKey: ['organization'] })
      onSaved?.(r.organization.id)
      onClose()
    },
  })
  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    save.mutate()
  }
  const attributeError = save.error instanceof ApiError && Object.keys(save.error.fields).some((k) => k.startsWith('custom_attributes.'))
  const generic = save.isError && !fieldError(save.error, 'name') && !fieldError(save.error, 'domains') && !attributeError
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={organization ? 'Organisatie bewerken' : 'Organisatie toevoegen'}>
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {generic && <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>}
        <Field label="Naam" error={fieldError(save.error, 'name')}>
          {(p) => <Input {...p} autoFocus maxLength={200} value={name} onChange={(e) => setName(e.target.value)} />}
        </Field>
        <fieldset className="flex flex-col gap-2">
          <legend className="mb-2 text-sm text-ink">Domeinen</legend>
          <p className="t-label">Mail van deze domeinen wordt automatisch aan de organisatie gekoppeld.</p>
          {domains.map((d, i) => (
            <div key={i} className="flex items-center gap-2">
              <Input
                aria-label={`Domein ${i + 1}`}
                placeholder="voorbeeld.nl"
                value={d}
                onChange={(e) => setDomains((cur) => cur.map((x, j) => (j === i ? e.target.value : x)))}
              />
              <IconButton label={`Domein ${i + 1} verwijderen`} onClick={() => setDomains((cur) => cur.filter((_, j) => j !== i))}>
                <X size={16} aria-hidden />
              </IconButton>
            </div>
          ))}
          {fieldError(save.error, 'domains') && <p className="text-sm text-danger-text">{fieldError(save.error, 'domains')}</p>}
          <div>
            <Button size="sm" disabled={domains.length >= 20} onClick={() => setDomains((cur) => [...cur, ''])}>
              <Plus size={14} aria-hidden />
              Domein toevoegen
            </Button>
          </div>
        </fieldset>
        <AttributeFields
          defs={defs.data ?? []}
          drafts={drafts}
          onChange={(k, v) => setDrafts((d) => ({ ...d, [k]: v }))}
          error={save.error}
        />
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
