import { useColorScheme } from 'react-native'

// The ron tokens of the web app (web/src/styles/ron.css with the "signal" accent from
// web/src/styles.css), so the app reads as the same product. Keep the two in step.

const light = {
  bg: '#ececec',
  surface: '#ffffff',
  surface2: '#f1f1f0',
  ink: '#0d0d0d',
  ink2: '#666666',
  ink3: '#858585',
  line: 'rgba(0, 0, 0, 0.07)',
  lineStrong: 'rgba(0, 0, 0, 0.14)',
  scrim: 'rgba(0, 0, 0, 0.5)',
}

const dark: typeof light = {
  bg: '#0b0c0d',
  surface: '#17191b',
  surface2: '#202326',
  ink: '#f2f2f2',
  ink2: 'rgba(255, 255, 255, 0.62)',
  ink3: 'rgba(255, 255, 255, 0.42)',
  line: 'rgba(255, 255, 255, 0.08)',
  lineStrong: 'rgba(255, 255, 255, 0.16)',
  scrim: 'rgba(0, 0, 0, 0.6)',
}

// One accent with one meaning: the customer waits on us. It is a fill, never text.
const shared = {
  accent: '#35e27a',
  accentInk: '#04150b',
  alert: '#ff4d3a',
  alertInk: '#0d0d0d',
  // Alert as text: the fill is too light for small text on white.
  alertText: '#b3261e',
}

export type Colors = typeof light & typeof shared

export const space = { 1: 4, 2: 8, 3: 12, 4: 16, 5: 24, 6: 32, 7: 48, 8: 64 } as const
export const radius = { s: 12, m: 24, l: 40, pill: 999 } as const
export const height = { s: 32, m: 44, l: 56 } as const
export const size = { xs: 12, s: 14, m: 16, l: 20, xl: 28, xxl: 40 } as const

// Weight is carried by the font file: the family names are the PostScript names of the bundled
// Urbanist files, which iOS and Android both resolve.
export const font = { light: 'Urbanist-Light', regular: 'Urbanist-Regular', medium: 'Urbanist-Medium' } as const

export function useColors(): Colors {
  return useColorScheme() === 'dark' ? { ...dark, ...shared } : { ...light, ...shared }
}
