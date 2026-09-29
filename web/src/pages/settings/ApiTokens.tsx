import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { KeyRound, Plus } from 'lucide-react'
import { type SubmitEvent, useState } from 'react'

import { CopyButton } from '../../components/CopyButton'
import { Dialog, DialogFooter } from '../../components/Dialog'
import { Badge, Button, Card, EmptyState, ErrorNotice, Field, Input, Page, PageHeader, Select, Skeleton, Table, TBody, Td, Th, THead, Tr } from '../../components/ui'
import { api } from '../../lib/api'
import { errorMessage, fieldError } from '../../lib/errors'
import { formatDateTime } from '../../lib/format'

type Scope = 'read' | 'write'

interface ApiToken {
  id: string
  name: string
  prefix: string
  scope: Scope
  expires_at: string | null
  last_used_at: string | null
  created_at: string
  user_id: string
  user_name?: string
  user_email?: string
}

const scopeLabel: Record<Scope, string> = { read: 'Alleen lezen', write: 'Lezen en schrijven' }

const expiryOptions = [
  { value: '', label: 'Verloopt niet' },
  { value: '30', label: 'Na 30 dagen' },
  { value: '90', label: 'Na 90 dagen' },
  { value: '365', label: 'Na 1 jaar' },
]

function TokenRows({ tokens, showOwner, onRevoke }: { tokens: ApiToken[]; showOwner: boolean; onRevoke: (t: ApiToken) => void }) {
  return (
    <Card flush>
      <Table>
        <THead>
          {showOwner && <Th>Gebruiker</Th>}
          <Th>Naam</Th>
          <Th className="hidden md:table-cell">Rechten</Th>
          <Th className="hidden lg:table-cell">Laatst gebruikt</Th>
          <Th className="hidden lg:table-cell">Verloopt</Th>
          <Th className="w-px">
            <span className="sr-only">Acties</span>
          </Th>
        </THead>
        <TBody>
          {tokens.map((t) => (
            <Tr key={t.id}>
              {showOwner && (
                <Td>
                  <div className="truncate text-ink">{t.user_name}</div>
                  <div className="truncate text-sm text-faint">{t.user_email}</div>
                </Td>
              )}
              <Td>
                <div className="truncate text-ink">{t.name}</div>
                <div className="font-mono text-sm text-faint">{t.prefix}…</div>
              </Td>
              <Td className="hidden md:table-cell">
                <Badge>{scopeLabel[t.scope]}</Badge>
              </Td>
              <Td className="hidden whitespace-nowrap text-muted lg:table-cell">{t.last_used_at ? formatDateTime(t.last_used_at) : 'Nog niet gebruikt'}</Td>
              <Td className="hidden whitespace-nowrap text-muted lg:table-cell">{t.expires_at ? formatDateTime(t.expires_at) : 'Nooit'}</Td>
              <Td>
                <div className="flex justify-end">
                  <Button size="sm" aria-label={`Token ${t.name} intrekken`} onClick={() => onRevoke(t)}>
                    Intrekken
                  </Button>
                </div>
              </Td>
            </Tr>
          ))}
        </TBody>
      </Table>
    </Card>
  )
}

function RevokeDialog({ token, admin, onClose }: { token: ApiToken; admin: boolean; onClose: () => void }) {
  const queryClient = useQueryClient()
  const revoke = useMutation({
    mutationFn: () => api('DELETE', admin ? `/tokens/${token.id}` : `/me/tokens/${token.id}`),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['api-tokens'] })
      onClose()
    },
  })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title="Token intrekken">
      <div className="flex flex-col gap-4">
        <p className="text-base text-muted">
          Toepassingen die het token {token.name} gebruiken, krijgen meteen geen toegang meer. Dit kan niet ongedaan worden gemaakt.
        </p>
        {revoke.isError && <ErrorNotice>{errorMessage(revoke.error)}</ErrorNotice>}
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button variant="danger" busy={revoke.isPending} onClick={() => revoke.mutate()}>
            Intrekken
          </Button>
        </DialogFooter>
      </div>
    </Dialog>
  )
}

function CreateDialog({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient()
  const [name, setName] = useState('')
  const [scope, setScope] = useState<Scope>('read')
  const [expiry, setExpiry] = useState('')
  const create = useMutation({
    mutationFn: () =>
      api<{ token: string }>('POST', '/me/tokens', {
        name,
        scope,
        expires_at: expiry ? new Date(Date.now() + Number(expiry) * 86_400_000).toISOString() : null,
      }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['api-tokens'] }),
  })
  const submit = (e: SubmitEvent) => {
    e.preventDefault()
    create.mutate()
  }

  if (create.isSuccess) {
    return (
      <Dialog open onOpenChange={(o) => !o && onClose()} title="Token aangemaakt">
        <div className="flex flex-col gap-4">
          <p className="text-base text-ink">Kopieer het token nu en bewaar het veilig. Het wordt niet opnieuw getoond.</p>
          <code className="rounded-md border border-line bg-subtle px-3 py-2 font-mono text-base break-all text-ink select-all">{create.data.token}</code>
          <div>
            <CopyButton text={create.data.token} label="Token kopiëren" />
          </div>
          <DialogFooter>
            <Button variant="primary" onClick={onClose}>
              Klaar
            </Button>
          </DialogFooter>
        </div>
      </Dialog>
    )
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title="Token aanmaken" description="Een token heeft nooit meer rechten dan jijzelf.">
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {create.isError && !fieldError(create.error, 'name') && !fieldError(create.error, 'scope') && !fieldError(create.error, 'expires_at') && (
          <ErrorNotice>{errorMessage(create.error)}</ErrorNotice>
        )}
        <Field label="Naam" help="Bijvoorbeeld de toepassing die het token gebruikt." error={fieldError(create.error, 'name')}>
          {(p) => <Input {...p} autoFocus maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />}
        </Field>
        <Field label="Rechten" error={fieldError(create.error, 'scope')}>
          {(p) => (
            <Select {...p} value={scope} onChange={(e) => setScope(e.target.value as Scope)}>
              <option value="read">Alleen lezen</option>
              <option value="write">Lezen en schrijven</option>
            </Select>
          )}
        </Field>
        <Field label="Vervalt" error={fieldError(create.error, 'expires_at')}>
          {(p) => (
            <Select {...p} value={expiry} onChange={(e) => setExpiry(e.target.value)}>
              {expiryOptions.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <DialogFooter>
          <Button onClick={onClose}>Annuleren</Button>
          <Button type="submit" variant="primary" busy={create.isPending} disabled={!name.trim()}>
            Aanmaken
          </Button>
        </DialogFooter>
      </form>
    </Dialog>
  )
}

export function ApiTokensPage() {
  const tokens = useQuery({ queryKey: ['api-tokens', 'mine'], queryFn: () => api<{ tokens: ApiToken[] }>('GET', '/me/tokens') })
  const [creating, setCreating] = useState(false)
  const [revoking, setRevoking] = useState<ApiToken | null>(null)

  return (
    <Page>
      <PageHeader breadcrumb="Persoonlijk"
        title="API-tokens"
        description="Met een token praat een andere toepassing namens jou met Echoo. Het token werkt zonder wachtwoord en zonder tweestapscode, dus behandel het als een wachtwoord."
        actions={
          <Button variant="primary" onClick={() => setCreating(true)}>
            <Plus size={16} aria-hidden />
            Token aanmaken
          </Button>
        }
      />
      {tokens.isPending ? (
        <Skeleton className="h-40" />
      ) : tokens.isError ? (
        <ErrorNotice>{errorMessage(tokens.error)}</ErrorNotice>
      ) : tokens.data.tokens.length === 0 ? (
        <Card>
          <EmptyState icon={<KeyRound size={20} />} title="Nog geen tokens" description="Maak een token aan om Echoo vanuit een andere toepassing te gebruiken." />
        </Card>
      ) : (
        <TokenRows tokens={tokens.data.tokens} showOwner={false} onRevoke={setRevoking} />
      )}
      {creating && <CreateDialog onClose={() => setCreating(false)} />}
      {revoking && <RevokeDialog token={revoking} admin={false} onClose={() => setRevoking(null)} />}
    </Page>
  )
}

export function AllApiTokensPage() {
  const tokens = useQuery({ queryKey: ['api-tokens', 'all'], queryFn: () => api<{ tokens: ApiToken[] }>('GET', '/tokens') })
  const [revoking, setRevoking] = useState<ApiToken | null>(null)

  return (
    <Page>
      <PageHeader breadcrumb="Werkruimte" title="API-tokens" description="Alle actieve tokens in deze werkruimte. Trek een token in als iemand vertrekt of een token is gelekt." />
      {tokens.isPending ? (
        <Skeleton className="h-40" />
      ) : tokens.isError ? (
        <ErrorNotice>{errorMessage(tokens.error)}</ErrorNotice>
      ) : tokens.data.tokens.length === 0 ? (
        <Card>
          <EmptyState icon={<KeyRound size={20} />} title="Geen actieve tokens" />
        </Card>
      ) : (
        <TokenRows tokens={tokens.data.tokens} showOwner onRevoke={setRevoking} />
      )}
      {revoking && <RevokeDialog token={revoking} admin onClose={() => setRevoking(null)} />}
    </Page>
  )
}
