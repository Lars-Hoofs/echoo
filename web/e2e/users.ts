import type { Locator } from '@playwright/test'

// With system mail configured (the CI stack, for invites.spec.ts) the "add user" dialog invites
// by email. Specs that sign the new user in with a temporary password switch to that first;
// without system mail the switch is not offered and the dialog already works that way.
export async function useTemporaryPassword(dialog: Locator): Promise<void> {
  const toggle = dialog.getByRole('button', { name: 'Tijdelijk wachtwoord tonen' })
  if ((await toggle.count()) > 0) await toggle.click()
}
