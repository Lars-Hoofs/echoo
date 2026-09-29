import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { type SubmitEvent, useState } from 'react'

import { Dialog, DialogFooter } from '../../components/Dialog'
import { Button, ErrorNotice, Field, Input } from '../../components/ui'
import { api, ApiError } from '../../lib/api'
import { attributeDefsQuery, attributeDrafts, attributesToPatch, type ContactFull } from '../../lib/contacts'
import { errorMessage, fieldError } from '../../lib/errors'
import { AttributeFields, EmailsEditor, emailRowsFrom, OrganizationSelect } from './fields'

// Creates a contact, or edits one when contact is given.
export function ContactDialog({
  contact,
  onClose,
  onSaved,
}: {
  contact?: ContactFull
  onClose: () => void
  onSaved?: (id: string) => void
}) {
  const defs = useQuery(attributeDefsQuery('contact'))
  const [name, setName] = useState(contact?.name ?? '')
  const [phone, setPhone] = useState(contact?.phone ?? '')
  const [emails, setEmails] = useState(() => emailRowsFrom(contact?.emails ?? []))
  const [organizationId, setOrganizationId] = useState(contact?.organization?.id ?? '')
  const [drafts, setDrafts] = useState<Record<string, string>>({})
  const [draftsSeeded, setDraftsSeeded] = useState(false)
  if (defs.data && !draftsSeeded) {
    setDraftsSeeded(true)
    setDrafts(attributeDrafts(defs.data, contact?.custom_attributes ?? {}))
  }
  const queryClient = useQueryClient()
  const save = useMutation({
    mutationFn: () => {
      const body = {
        name,
        phone,
        emails: emails.filter((e) => e.email.trim() !== '').map((e) => ({ email: e.email.trim(), primary: e.primary })),
        organization_id: organizationId || null,
        custom_attributes: attributesToPatch(defs.data ?? [], drafts, contact?.custom_attributes ?? {}),
      }
      return contact
        ? api<{ contact: ContactFull }>('PATCH', `/contacts/${contact.id}`, body)
        : api<{ contact: ContactFull }>('POST', '/contacts', body)
    },
    onSuccess: async (r) => {
      await queryClient.invalidateQueries({ queryKey: ['contacts'] })
      await queryClient.invalidateQueries({ queryKey: ['contact'] })
      onSaved?.(r.contact.id)
      onClose()
    },
  })
  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    save.mutate()
  }
  const generic =
    save.isError && !['name', 'phone', 'emails', 'organization_id'].some((f) => fieldError(save.error, f)) && !hasAttributeError(save.error)
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={contact ? 'Contact bewerken' : 'Contact toevoegen'}>
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {generic && <ErrorNotice>{errorMessage(save.error)}</ErrorNotice>}
        <Field label="Naam" error={fieldError(save.error, 'name')}>
          {(p) => <Input {...p} autoFocus maxLength={200} value={name} onChange={(e) => setName(e.target.value)} />}
        </Field>
        <Field label="Telefoon" error={fieldError(save.error, 'phone')}>
          {(p) => <Input {...p} type="tel" maxLength={50} value={phone} onChange={(e) => setPhone(e.target.value)} />}
        </Field>
        <EmailsEditor rows={emails} onChange={setEmails} error={fieldError(save.error, 'emails')} />
        <OrganizationSelect
          value={organizationId}
          current={contact?.organization ?? null}
          onChange={setOrganizationId}
          error={fieldError(save.error, 'organization_id')}
        />
        <AttributeFields
          defs={defs.data ?? []}
          drafts={drafts}
          onChange={(k, v) => setDrafts((d) => ({ ...d, [k]: v }))}
          error={save.error}
        />
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button type="submit" variant="primary" busy={save.isPending}>
            Opslaan
          </Button>
        </DialogFooter>
      </form>
    </Dialog>
  )
}

function hasAttributeError(err: unknown): boolean {
  return err instanceof ApiError && Object.keys(err.fields).some((k) => k.startsWith('custom_attributes.'))
}
