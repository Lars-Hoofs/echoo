import { Outlet } from '@tanstack/react-router'

export function SettingsShell() {
  return (
    <div className="relative h-full overflow-y-auto px-4 py-8 md:px-8">
      <Outlet />
    </div>
  )
}
