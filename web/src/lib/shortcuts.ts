// Every keyboard shortcut of the app, for the overview dialog. Keep in step with
// pages/shell/useGlobalShortcuts.ts, pages/inbox/useInboxShortcuts.ts, the list keys in
// pages/inbox/InboxPage.tsx, RichEditor and RecipientField.

export interface Shortcut {
  keys: string[]
  label: string
}

export interface ShortcutGroup {
  title: string
  shortcuts: Shortcut[]
}

export function isMac(): boolean {
  return /Mac|iPhone|iPad/.test(navigator.platform)
}

export function modKey(): string {
  return isMac() ? '⌘' : 'Ctrl'
}

export function shortcutGroups(mod: string): ShortcutGroup[] {
  return [
    {
      title: 'Overal',
      shortcuts: [
        { keys: [mod, 'K'], label: 'Opdrachtenbalk openen' },
        { keys: ['/'], label: 'Zoeken' },
        { keys: ['?'], label: 'Sneltoetsen tonen' },
        { keys: ['g', 'i'], label: 'Naar mijn inbox' },
        { keys: ['g', 'a'], label: 'Naar alle gesprekken' },
        { keys: ['g', 's'], label: 'Naar instellingen' },
      ],
    },
    {
      title: 'Gesprekkenlijst',
      shortcuts: [
        { keys: ['j'], label: 'Volgend gesprek' },
        { keys: ['k'], label: 'Vorig gesprek' },
        { keys: ['o'], label: 'Gesprek openen' },
        { keys: ['x'], label: 'Gesprek selecteren' },
        { keys: ['Esc'], label: 'Terug naar de lijst' },
      ],
    },
    {
      title: 'Gesprek',
      shortcuts: [
        { keys: ['e'], label: 'Sluiten of heropenen' },
        { keys: ['w'], label: 'Op wachtend zetten' },
        { keys: ['r'], label: 'Antwoorden' },
        { keys: ['n'], label: 'Notitie schrijven' },
        { keys: ['a'], label: 'Toewijzen' },
        { keys: ['l'], label: 'Labels wijzigen' },
        { keys: ['p'], label: 'Prioriteit wijzigen' },
        { keys: ['s'], label: 'Uitstellen' },
      ],
    },
    {
      title: 'Schrijven',
      shortcuts: [
        { keys: [mod, '↵'], label: 'Verzenden' },
        { keys: [mod, 'B'], label: 'Vet' },
        { keys: [mod, 'I'], label: 'Cursief' },
        { keys: ['@'], label: 'Collega noemen in een notitie' },
        { keys: ['↵'], label: 'Ontvanger toevoegen (ook met komma of puntkomma)' },
      ],
    },
    {
      title: 'Opdrachtenbalk en keuzelijsten',
      shortcuts: [
        { keys: ['↑', '↓'], label: 'Navigeren' },
        { keys: ['↵'], label: 'Kiezen' },
        { keys: ['Esc'], label: 'Sluiten' },
      ],
    },
  ]
}
