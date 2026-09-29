import { useSuspenseQuery } from '@tanstack/react-query'
import { Outlet } from '@tanstack/react-router'
import { useEffect } from 'react'

import { applyTheme, meQuery } from '../lib/session'

export function AuthLayout() {
  const { data: me } = useSuspenseQuery(meQuery)
  useEffect(() => {
    applyTheme(me.user.theme)
  }, [me.user.theme])
  return <Outlet />
}
