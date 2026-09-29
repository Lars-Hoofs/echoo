import { type Permission, permissionGroups, togglePermission } from '../lib/permissions'

// PermissionPicker shows the permissions in groups. Ticking one ticks what it needs; what the
// user may not hand out is disabled, with the reason as help text.
export function PermissionPicker({
  value,
  onChange,
  readOnly = false,
  canSet = () => true,
}: {
  value: readonly Permission[]
  onChange?: (next: Permission[]) => void
  readOnly?: boolean
  canSet?: (p: Permission) => boolean
}) {
  return (
    <div className="flex flex-col gap-5">
      {permissionGroups.map((group) => (
        <fieldset key={group.id} className="flex flex-col gap-2.5">
          <legend className="mb-1 text-sm font-medium text-faint">{group.label}</legend>
          {group.permissions.map((p) => {
            const locked = readOnly || !canSet(p.key)
            return (
              <label key={p.key} className={`flex items-start gap-3 ${locked ? 'cursor-default' : 'cursor-pointer'}`}>
                <input
                  type="checkbox"
                  checked={value.includes(p.key)}
                  disabled={locked}
                  onChange={(e) => onChange?.(togglePermission(value, p.key, e.target.checked))}
                  className="mt-1 size-4 shrink-0 accent-(--accent) disabled:opacity-60"
                />
                <span className="min-w-0">
                  <span className="block text-base text-ink">{p.label}</span>
                  <span className="block text-sm text-faint">{p.description}</span>
                </span>
              </label>
            )
          })}
        </fieldset>
      ))}
    </div>
  )
}
