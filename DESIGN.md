# Echoo design

Status: proposal, awaiting approval (step 1). UI copy is Dutch only; this document is English
for developers, with Dutch examples where copy is concerned.

## 1. Direction

Calm, businesslike, fast. Echoo is a tool people keep open for eight hours. Restyled on
2026-09-29 to the Rondesignlab system (skill `rondesignlab-style`): quiet grey, white and black
surfaces, one flat accent, light numerals and pill controls. `web/src/styles/ron.css` is the
system's stylesheet, imported as a CSS layer between Tailwind's base and utilities; its tokens
are the single source of colour, type, radius and motion. Kind: product, so the app fills the
viewport (no studio background), and the palette is "signal".

- **Neutral surfaces, one accent.** Roughly 92% of the pixels are grey, white or ink. The accent
  (signal green `#35E27A`) has one meaning across the whole app: **the customer waits on us**
  (open conversations that need our reply, unread counts, the reply action). Nothing else is
  green: not progress, not toggles, not links, not "success".
- **Red is for failure only.** `--alert` (`#FF4D3A`) marks errors, failed or bounced sends and
  breached SLAs. Accent and alert are more than 30 degrees apart in hue.
- **Flat.** No gradients, no glow, no shadows on cards. Structure is 1 px rules and small shifts
  in surface tone. Shadow and blur appear on floating layers only (menus, dialogs, palette).
- **Numbers are the design.** Figures use ron's `Num`: weight 300, small raised currency, grey
  decimals and unit, a short grey label beneath.
- **Pills and circles.** Buttons, inputs, tabs, tags and menu items are pills; icon buttons and
  avatars are circles; cards are 24 px, large areas 40 px.
- **Status by shape first, colour second.** Every status has a distinct glyph and a text label.
- **The mail is the content.** Chrome recedes; the conversation column has the most contrast and
  the most space.
- **Motion means something.** Entrances are a focus pull (`Reveal`, blur to sharp), presses darken
  filled controls. It only runs under `html.motion`, which `lib/motion.ts` sets unless the
  viewer asked for reduced motion.

## 2. References

**Primary reference (added 2026-09-28):** the user's screenshot of Chatwoot's agent UI. It
defines the structure Echoo follows: a persistent sidebar (search, Mijn inbox, Gesprekken,
Teams, Mailboxen, user at the bottom), a conversation list with Mijn / Zonder toewijzing / Alle
tabs, a conversation pane with message bubbles (inbound an outlined card, outbound a filled
neutral one, never accent) and a
composer with Antwoorden / Notitie tabs, and a contact panel on the right. What Echoo does
differently: its own palette and type (ron tokens, Urbanist), no social channel icons (mail only),
initials instead of photos, internal notes as full-width `surface-2` blocks with a thin ink rule
and the label "Interne notitie", and delivery states (queued, failed, onzeker) shown inside and
under the bubble. Where the notes
below say "no chat bubbles", this revision supersedes them.

## 2b. Mobbin research

Searched with the Mobbin MCP per screen type, by app name and by pattern, screens and flows.

**Gaps found, reported as required:**
- **Modal (modal.com) is barely covered on Mobbin.** Queries for its dashboards, apps list and
  usage pages returned one Modal screen (the Active Resources dialog). To cover "calm technical
  dashboard" I added Supabase, Railway, Perplexity, Vercel, Firecrawl and Profound, which show
  the same qualities. If you have a specific Modal page in mind, a screenshot would help.
- **Raycast** returned no web screens; command palette references come from Vapi, Juicebox,
  Magnific and Mintlify, plus Linear's in-list menus.
- **Stripe** was not needed: Linear's settings covered navigation, member tables and permission
  forms well. I can add a Stripe pass for the rule builder in phase 5 if you want.

### 2b.1 Dashboard (rapportage, SLA, volume)

| Reference | What we take | What Echoo does differently |
|---|---|---|
| [Modal – Active Resources](https://mobbin.com/screens/e5dd3f89-3181-4b4e-9516-2ad5114c2265) | Chart on top, table below that shares the legend; per-row sparkline in the table; resource selector + time range as two compact controls in one line. | Modal's series colours (pink, green, yellow on black) are loud. Echoo uses a blue ramp plus one neutral, and highlights only the series the user hovers. |
| [Supabase – Edge function overview](https://mobbin.com/screens/1d51f335-116b-4d52-89cb-49dbf67c7caf) | Segmented period control ("15 min · 1 hour · 3 hours · 1 day") with the resulting range spelled out next to it; headline number above each chart. | No two-column card grid. Echoo puts KPIs in a single ruled strip, charts full-width below. |
| [Railway – Observability](https://mobbin.com/screens/6e416c9f-4f02-44ed-9c2c-e08e3054fb71) | Dense metric blocks, dark theme that uses surface steps instead of shadows. | Railway's red-tinted log block is alarming; Echoo shows SLA breaches as a list with a glyph and a count, not a red panel. |
| [Perplexity – Analytics](https://mobbin.com/screens/3956c5cb-bcc4-4a6f-bc0d-c2149bbb7822) | "Data in UTC · bijgewerkt om …" line under the title; 7d/30d/90d pills; three KPIs with small labels. | KPIs are not boxed separately; one row, divided by vertical rules. |
| [Vercel – Analytics detail](https://mobbin.com/screens/677f08e4-f9fd-46da-9e2f-18420bf05d57) | Right-aligned numeric columns with a thin inline bar behind the label. Used for "per agent" and "per team" tables. | Bars use `--accent-subtle`, never full-strength colour. |
| [Firecrawl – Usage flow](https://mobbin.com/flows/718c6724-a237-4512-a05e-441c74e14295) | Numbered section markers for long report pages. | Echoo keeps one page per report scope; sections are plain headings. |
| [Profound – Logs flow](https://mobbin.com/flows/4b99fb33-72a9-4080-a885-b235f4019ead) | Filter bar (period, filters, export) above a dense table; row click opens a side sheet. | Same pattern for the admin "Taken" (failed jobs) and audit log views. |

Dashboard layout:

```
Rapportage                                  [Alle mailboxen v] [Vandaag|7 d|30 d|90 d|Aangepast]
Periode 1 t/m 28 september · tijden in Europe/Amsterdam
────────────────────────────────────────────────────────────────────────────────────────────
Nieuwe gesprekken │ Eerste reactie (mediaan) │ Oplostijd (mediaan) │ Binnen SLA │ Open nu
      1.284  +6% │               42 min     │        6 u 10 min   │   94,1 %   │   87
────────────────────────────────────────────────────────────────────────────────────────────
Volume per dag                                                        ▁▂▃▅▃▂▁▂▄▆▅▃  (bars)
────────────────────────────────────────────────────────────────────────────────────────────
Per agent              Gesprekken  Eerste reactie  Oplostijd  Binnen SLA
Sanne de Vries         ████ 212        31 min       4 u 02    97 %
...
```

### 2b.2 Inbox / conversation list

| Reference | What we take | What Echoo does differently |
|---|---|---|
| [Linear – issue list grouped by status, bulk selection](https://mobbin.com/screens/be6c4ee4-aa93-42b4-89b3-dcfc8386f022) | Row density (~36 px), sticky group headers with count, right-aligned meta (labels, assignee, date), bulk selection with a count and "Acties". | Linear tints the group header rows. Echoo uses a hairline and a small caps label. Bulk actions replace the list toolbar in place instead of a floating pill, so nothing covers rows. |
| [Linear – dark list](https://mobbin.com/screens/e142df2a-3527-499c-8f81-1b715947ac0c) | Dark theme hierarchy with only three surface steps. | Our dark neutrals are warm, not neutral grey. |
| [Linear – display options](https://mobbin.com/screens/94bb4d3b-a8e3-41e8-b8f1-b82d1f904b03) | Grouping/ordering/properties popover. | Reduced to what support needs: group by (status, toewijzing, geen), sort (laatste activiteit, SLA-deadline, aangemaakt), density (compact/ruim). |
| [Front – shared inbox](https://mobbin.com/screens/29be437a-6d62-4b45-9f8d-1f1913a724ea) | Sidebar: personal (Open, Later, Gesloten) above shared mailboxes with counts. | Front's list rows are two/three lines with colourful avatars. Echoo's default is one line: sender · subject · preview (faint) · SLA · assignee initials · time; "ruim" density gives two lines. |

Row anatomy (compact, 36 px):
`[☐] [status glyph] Afzender (semibold if unread) · Onderwerp — preview …   [labels] [SLA 25 min] [SV] 14:02`

### 2b.3 Conversation view with threads

| Reference | What we take | What Echoo does differently |
|---|---|---|
| [Front – message with system events and comment bar](https://mobbin.com/screens/29be437a-6d62-4b45-9f8d-1f1913a724ea) | System events ("Toegewezen aan Sanne door een regel") as single centred-muted lines between messages; internal comment input always visible at the bottom. | Events are left-aligned in the message column with a small glyph, and 3+ consecutive events collapse into "4 wijzigingen". |
| [Front – internal discussion](https://mobbin.com/screens/70704201-4645-4520-b6e4-3b8cb29f755d) | Notes are clearly for the team only, with "zichtbaar voor" hint. | No chat bubbles. A note is a full-width block with a 3 px Decor Yellow left rule, `--note-bg` background and the label "Interne notitie" so it cannot be mistaken for a customer mail, even in greyscale. |
| [Superhuman – collapsed thread with inline reply](https://mobbin.com/screens/ac146694-19c7-4bbd-81dc-409591cebd20) | Older messages collapsed to one line (sender, snippet, time); the newest expanded; reply opens inline under the last message, not in a modal. Quoted text collapsed behind "…". | Superhuman centres a narrow column with lots of air. Echoo left-aligns, caps reading width at 72ch and keeps the right context panel (contact, SLA, details) available with `]`. |
| [Superhuman – @mention in comment](https://mobbin.com/screens/f0800dd4-d71a-4bd8-a00c-9fe16434cb4f) | Mention autocomplete as a plain name/address list. | Only users with access to this mailbox are listed. |

HTML mail renders on a light "paper" surface in both themes by default, because most HTML mail
hard-codes white backgrounds and dark text; inverting it breaks logos and signatures.
Plain-text mail follows the app theme.

### 2b.4 Composer / editor

| Reference | What we take | What Echoo does differently |
|---|---|---|
| [Front – template picker with preview](https://mobbin.com/screens/0d2a8ff4-826f-4046-80a0-a218550db62a) | Searchable template list with a preview pane beside it. | Opened with `/` in the editor or `⌘/`; variables (stored as `{{contact.first_name}}`) are shown as inline chips with Dutch names ("Voornaam contactpersoon") that turn `--st-sla-risk` when they cannot be resolved. |
| [Front – empty composer with "/" hint and signature](https://mobbin.com/screens/b7b4cbdb-1376-40ca-b881-032d57082097) | Placeholder teaches the shortcut; signature is visible below a separator. | Signature is a non-editable block with "Handtekening wijzigen"; per-mailbox signature switches when the From mailbox changes. |
| [Front – formatting toolbar](https://mobbin.com/screens/c487d4e5-a7e1-44ef-b47d-8cb3012d1747) | Formatting only on demand. | No font family or size pickers (they produce messy mail). Toolbar: vet, cursief, link, lijst, genummerde lijst, citaat, bijlage. |
| [Front – create template](https://mobbin.com/screens/4c587c28-69a6-4444-972e-a3feda5c25ec) | Name, subject, access scope, body in one small dialog. | Scope choices match our model: persoonlijk, team, mailbox, iedereen. |

The composer is docked in the conversation column. Send is `⌘↵`; the send button is split:
"Versturen" and a menu with "Versturen en sluiten", "Versturen en wachten", "Later versturen".
After sending, a toast "Verstuurd · Ongedaan maken" is live for the configured send delay.

### 2b.5 Command palette and shortcuts

| Reference | What we take | What Echoo does differently |
|---|---|---|
| [Vapi – palette with sections](https://mobbin.com/screens/593d7acd-2e16-4365-bcd6-02ce52f48f3b) | Sections (Acties, Recent, Pagina's), secondary text after an em dash, footer with key hints and result count. | Light and dark both; no heavy dimming of the page behind (40% scrim). |
| [Magnific – shortcut badges per row](https://mobbin.com/screens/14ceb943-f04a-460f-b4f5-2ebd78d74aff) | Each action shows its shortcut on the right. | Shortcut badges in the system monospace, `--text-faint`, 1 px border. |
| [Juicebox – footer hints](https://mobbin.com/screens/2af813bf-0129-45d1-81ed-069edee76e16) | "↵ openen · ↑↓ navigeren · Tab sectie" footer. | Same, in Dutch. |
| [Linear – list context menu](https://mobbin.com/screens/3f36e39e-2b9e-4145-bee2-f43a3cf21f7b) | Context-aware menu items for the selection. | The palette is context-aware: with a conversation open, its actions (Sluiten, Toewijzen aan…, Label…) come first. |

Default shortcuts: `j`/`k` next/previous, `↵`/`o` open, `Esc` back, `e` sluiten, `w` wachtend,
`r` antwoorden, `n` notitie, `a` toewijzen, `l` label, `s` uitstellen, `p` prioriteit, `x` selecteren,
`/` zoeken, `g i` inbox, `g d` rapportage, `⌘K` palette, `?` overzicht sneltoetsen.
Single-key shortcuts are off while focus is in a text field.

### 2b.6 Filters and saved views

| Reference | What we take | What Echoo does differently |
|---|---|---|
| [Linear – filter menu](https://mobbin.com/screens/6d0577b3-4b12-44bb-a816-39997cc4a080) and [nested status submenu](https://mobbin.com/screens/d1d26f7d-e1e5-490f-ab4f-c96dd12854c1) | Type-to-filter menu of properties, nested value lists with counts. | Properties are support-specific: status, mailbox, toegewezen aan, team, label, prioriteit, SLA-status, contact, organisatie, heeft bijlage, periode. |
| [Linear – saved view header](https://mobbin.com/screens/b230cf2f-37bf-4621-8aab-e50003d63813) | Filter chips under the title ("Priority is High ×"), "Opslaan in: Persoonlijk". | Chips read as sentences in Dutch: "Status is Open", "Toegewezen aan is mij". |
| [Attio – filter chip with Save/Discard](https://mobbin.com/screens/94602448-d6fc-47df-a6eb-60619348d8ff) | When a saved view is modified: "Wijzigingen verwerpen · Opslaan" appears in the header. | Same; plus "Opslaan als nieuwe weergave". |

Search syntax in the search field mirrors the chips: `status:open van:@bedrijf.nl label:factuur "exacte zin"`.

### 2b.7 Settings and administration

| Reference | What we take | What Echoo does differently |
|---|---|---|
| [Linear – settings navigation and permission rows](https://mobbin.com/screens/5fefa82b-2268-4797-9788-5891ecd92d6a) | Separate settings shell with "Terug naar app", grouped nav (Account, Werkruimte, Beheer), each setting as a row: label + help text left, control right. | Groups: Persoonlijk (Profiel, Beveiliging, Handtekeningen, Meldingen), Werkruimte (Mailboxen, Teams, Gebruikers, Labels, Standaardantwoorden), Automatisering (Regels, SLA), Beheer (API-tokens, Webhooks, Taken, Auditlog, Privacy en retentie). |
| [Linear – members table](https://mobbin.com/screens/c1bf83ff-9577-4fe6-aff9-0230030944ee) | Plain table: naam, rol, teams, laatst actief. | Adds 2FA state column so admins can see who has not enrolled. |
| [Linear – confirmation toast](https://mobbin.com/screens/ae99a607-158a-4642-a69f-00c024381bd0) | Small toast with title + one line. | Bottom-left, auto-dismiss 5 s, pauses on hover, always an action if undo is possible. |

Mailbox settings show the sync state as a status line with the last success time, not a
coloured badge alone: "Verbonden via IMAP IDLE · laatst gesynchroniseerd 14:02".

The rule builder is sentence-based: "Als [een nieuw gesprek binnenkomt] en [afzender] [eindigt op]
[@leverancier.nl], dan [label toevoegen] [Leverancier]." Rows are added with "+ Voorwaarde" and
"+ Actie".

### 2b.8 Empty, error and loading states, onboarding

| Reference | What we take | What Echoo does differently |
|---|---|---|
| [OpenAI Platform – dashboard flow, empty evaluations](https://mobbin.com/flows/364f173f-c59c-481d-bdf3-46b229775b4a) | One icon, one sentence, one primary action, centred in the pane. | No icon at all in list empty states; one line of text and an action link. Icons only for first-run setup. |
| [Linear – short backlog](https://mobbin.com/screens/fd1b4d88-f021-49a3-98af-4cd3a87e1d29) | A short list is just a short list; no filler. | Same. |

Onboarding is a checklist in the empty inbox for the owner: "Mailbox koppelen", "Team aanmaken",
"Collega's uitnodigen", "2FA inschakelen". It disappears when done.

Loading: skeleton rows with the exact row geometry (no shimmer when `prefers-reduced-motion`),
and nothing at all for loads under 150 ms. Spinners only inside buttons during a submit.

## 3. Colour

Revised on 2026-09-29: ron tokens replace the earlier cool-grey-plus-blue palette. ron.css owns
the neutrals (light in `:root`, dark in `[data-theme='dark']`, and the same dark values under
`prefers-color-scheme` when the user's theme is "Systeem"). `web/src/styles.css` sets the accent
and aliases the app's older semantic names onto ron's tokens, so `bg-surface`, `text-muted`,
`border-line` and the rest keep working.

### 3.1 Rules

- **One accent, one meaning** (see section 1). It is always a fill with `--accent-ink` text
  (11.0 : 1). Green on white is 1.7 : 1, so the accent is never text: `--accent-text` is ink, and
  a small accent dot must sit next to a text label.
- **Alert.** Fill `--alert` with `--alert-ink` (`#0D0D0D`, 5.9 : 1; ron's white is 3.3 : 1). As
  text, `--alert-text` mixes 70% alert with ink (4.9 : 1 or better on every light surface, 4.8 : 1
  or better on dark).
- **No yellow.** At-risk SLA and uncertain sends are ink fills with a glyph; only a breach is red.
- **Labels are neutral.** Eight label colours map to eight greys of falling lightness (`--lb-*`),
  shown as a small swatch next to the name in a neutral pill. The name identifies the label.
- **Avatars are neutral**: initials on a grey circle.
- **Paper.** HTML mail is drawn on `#FFFFFF` in both themes (`--paper`).
- **Status never by colour alone** (glyph + label, see 3.4).

### 3.2 Palette

| ron token | Light | Dark | Use |
|---|---|---|---|
| `--surface-2` (`--bg-app`, `--bg-subtle`) | `#F1F1F0` | `#202326` | Page behind the sidebar, hover, table wells |
| `--surface` (`--bg-surface`, `--bg-list`) | `#FFFFFF` | `#17191B` | The floating main surface, cards |
| `--bg-float` | `#FFFFFF` | `#202326` | Menus, dialogs, toasts |
| `--bg` | `#ECECEC` | `#0B0C0D` | Text on ink fills |
| `--ink` (`--text`) | `#0D0D0D` | `#F2F2F2` | Text, active nav, primary buttons |
| `--ink-2` (`--text-muted`, `--text-faint`) | `#666666` | white at 62% | All secondary and small text |
| `--ink-3` | `#858585` | white at 42% | Large numerals, rules, chart hairlines only |
| `--line` (`--border`, `--bg-selected`) | black at 7% | white at 8% | Dividers, selected row |
| `--line-strong` (`--border-strong`, `--border-input`, `--bg-active`) | black at 14% | white at 16% | Input borders, pressed |
| `--accent` / `--accent-ink` | `#35E27A` / `#04150B` | same | See 3.1 |
| `--alert` / `--alert-ink` | `#FF4D3A` / `#0D0D0D` | same | Errors, failed sends, breached SLA |

Measured contrast (WCAG 2.x): `--ink-2` on bg / surface-2 / surface is 4.9 / 5.1 / 5.7 : 1 in
light and 6.9 to 7.7 : 1 in dark. `--ink-3` is 3.1 to 3.7 : 1 in light and 3.9 to 4.1 : 1 in
dark, so it is never used for small text; `--text-faint` is therefore `--ink-2`, and hierarchy
comes from size. Input borders are ron's `--line-strong` (about 1.4 : 1); fields are also
identified by their label and shape, and focus turns the border to ink. `check_system.py` from
the skill passes on ron.css and the palette blocks.

Dark is designed, not inverted: the page is the lighter surface, the main surface floats darker
above it, and floating layers use `--surface-2` with a 1 px border because shadows are invisible.

### 3.3 Aliases

| App token | Maps to |
|---|---|
| `--bg-app`, `--bg-subtle` | `--surface-2` |
| `--bg-surface`, `--bg-list` | `--surface` |
| `--bg-selected` / `--bg-active` | `--line` / `--line-strong` |
| `--text`, `--text-muted`, `--text-faint` | `--ink`, `--ink-2`, `--ink-2` |
| `--border`, `--border-strong`, `--border-input` | `--line`, `--line-strong`, `--line-strong` |
| `--accent-text`, `--accent-subtle`, `--accent-hover` | `--ink`, `--surface-2`, `--accent` |
| `--on-accent` | `--accent-ink` |
| `--danger`, `--on-danger`, `--danger-text`, `--danger-subtle` | `--alert`, `--alert-ink`, `--alert-text`, alert at 10% on surface |
| `--signal`, `--on-signal` | `--ink`, `--bg` (was yellow) |
| `--note-bg`, `--brand` | `--surface-2`, `--accent` |
| `--chart-1..5` | ink at 100, 70, 45, 28 and 16% over the surface |
| `--lb-*` | ink at 92 to 16% over the surface, one per label colour |

### 3.4 Status colours

Each status has a glyph, a label and a fill. Open is the accent (the customer waits on us);
Wachtend, Gesloten and Spam are neutral tags told apart by glyph and label; SLA at risk is an ink
tag with a clock; a breach or a failed send is an alert tag with a warning triangle. The values
are the `--st-*` tokens in `web/src/styles.css`.

## 4. Typography

**Urbanist** at 300, 400 and 500, self-hosted through `@fontsource/urbanist` (Latin and Latin
Extended subsets only; ë, ï, é and the European names in customer mail are covered). No font CDN.
Urbanist is ron's approximation of the studio's typeface. Monospace, for shortcut keys, tokens,
headers and codes, is the system stack (`ui-monospace`); no mono font is shipped.

ron's scale is the only scale: 12, 14, 16, 20, 28, 40, 64, 96 px (on screens up to 900 px the
three largest drop to 56, 44 and 32).

| Tailwind | Size | Use |
|---|---|---|
| `text-xs`, `text-sm` | 12 | Labels, meta, table headers, chart axes (`t-label`) |
| `text-base` | 14 | UI default, list rows (`t-body`) |
| `text-read`, `text-lg` | 16 | Message bodies, composer, dialog inputs |
| `text-xl` | 20 | Card and section titles (`t-title`) |
| `text-h3` / `text-kpi` | 28 | Page titles (`t-h3`), small figures |
| `text-h2`, `text-h1`, `text-display` | 40, 64, 96 | Large figures only |

Weight: 300 from 28 px up, 400 below, 500 for the logo and for unread emphasis (the one place
weight carries the accent's meaning). There is no 600: `font-semibold` resolves to 500. Headings
are set by size and weight 300, never bold. Letter-spacing follows ron (-0.01em body, tighter
when large). Columns of figures use `tabular-nums`.

**Numerals.** `components/Num.tsx` renders ron's `.num`: integer in ink, raised currency and grey
decimals and unit. Numbers are formatted with `splitNumber` in `lib/format.ts` (Dutch notation:
`1.284`, `94,1`). Sizes s, m, l, xl are 28, 40, 64 and 96 px.

## 5. Space, radius, borders, elevation

- Spacing scale (px): 4, 8, 12, 16, 24, 32, 48, 64, 96 (ron's `--s-*`). Tailwind's 4 px unit
  gives the same values for `1, 2, 3, 4, 6, 8, 12, 16, 24`; avoid the half steps.
- Radius: `rounded-md` 12 px (textareas, notices, skeletons, small surfaces), `rounded-lg` 24 px
  (cards, dialogs, menus, the main surface), `rounded-xl` 40 px (large areas), `rounded-full`
  (buttons, inputs, tags, tabs, menu items, avatars, switches).
- Control heights: 32 (`sm`, 44 on phones), 44 (default), 56 (`lg`).
- Borders: 1 px everywhere; 2 px only for focus rings.
- Elevation: cards are flat with a 1 px line. Floating layers use `--bg-float`, a 1 px line and
  `--shadow-float`; dialogs and the command palette dim the page with a blurred scrim.
- Focus: ron's `:focus-visible`, a 2 px ink outline at 2 px offset. Never removed.
- Icons: lucide-react, set globally to 20 px, stroke 1.5, `currentColor` (`LucideProvider` in
  `main.tsx`; `svg.lucide { stroke-width: 1.5 }` enforces the stroke). Small inline icons in tags
  and menus may pass a smaller `size`. No unicode glyphs as icons.

### 5.1 Components

All in `web/src/components`, on ron classes with Tailwind utilities for layout only.

- `ui.tsx`: `Button` (primary = ink fill, `accent` only for reply and "needs us", secondary =
  line, ghost, danger = alert fill; sizes sm/md/lg), `IconButton` (round `.icon-btn`), `Input`,
  `Select` (pills), `Textarea` (12 px), `Field` (12 px grey label), `Card` (`.card-line`, 24 px),
  `Badge` (`.tag`; accent and danger tones are for their meanings only), `Switch` (ink when on),
  `Segmented` (ron tabs, chosen option solid ink), `Kbd`, table primitives (grey 12 px headers,
  hairline rows), `EmptyState`, `Skeleton`, `Page` and `PageHeader` (28 px light title, small
  grey kicker), `Wordmark`.
- `Num`, `Reveal` (focus-pull entrance, `--i` staggers siblings), `Avatar`, `Logo` (accent tile
  with "e" and the wordmark at weight 500), `StatusBadge`, `LabelChip`, `SlaTimer`, `Toast`,
  `Dialog`, `ActionMenu`, `CopyButton`, `AuthCard`.
- `charts/`: 1.25 px ink lines, open-circle points, hatched bars where the hovered or focused bar
  turns solid ink, no legend boxes (a segmented bar lists label and value side by side).
- Shell (`pages/AppShell.tsx`, `pages/shell/`): a grey page with the sidebar on it and the main
  surface floating beside it (24 px radius, 1 px line). The sidebar is 272 px: workspace row
  (logo, "Echoo", mailbox subtitle, notifications), search field with a `/` key tag, the
  navigation (only this scrolls, with a fade), then the foot: settings, a compact card with the
  user's open conversations against their capacity (`users.max_open`; "zonder limiet" when unset)
  and the account row. Active items are solid ink pills, counts are tags, and only the "Mijn
  inbox" unread count is accent. Below 1000 px the sidebar becomes a sheet behind a top bar.

### Inbox and conversation (ron)

- The accent means "the customer waits on us" here as everywhere: the unread dot in a list row,
  the accent ring on the status glyph of a read conversation that is still unanswered (never both
  on one row), the open status tag, and the one accent button, "Versturen". A sent reply answers
  that wait, so outbound bubbles are neutral (`surface-2`, right-aligned) and inbound mail is an
  outlined card (24 px radius, line). Priority is a neutral tag.
- Alert is only for a breached SLA ("12 min te laat") and for a send that failed, bounced or is
  uncertain (tag with label under the bubble and the mailserver text below it). An SLA at risk is
  a neutral tag with a clock ("Nog 25 min").
- List row: neutral avatar, name, preview in `ink-2`, time and "mailbox · assignee" as `t-label`;
  unread is the dot plus weight 500 and screen-reader text. Tabs are ron tabs with the counts as
  plain numbers, because three tags do not fit in the 360 px column.
- Conversation header: contact name and `#number · mailbox` as `t-label`, subject as `t-title`,
  then pills (Macro's, Uitstellen, more) and the ink "Sluiten" split button; on phones the pills
  turn into round 44 px icon buttons.
- The contact panel opens with a large neutral avatar and a small card with real numbers from the
  contact endpoints: conversations, open, and waiting on us (counted from the first page of the
  contact's conversations, shown with a "+" when there are more).

## 6. Layout

Desktop (≥ 1200 px): three panes.

```
┌────────────┬──────────────────────┬──────────────────────────────────────┬──────────┐
│ Navigation │ Lijst                │ Gesprek                              │ Details  │
│ 224 px     │ 360–480 px, resizable│ flexible, reading width ≤ 72ch       │ 300 px   │
│ (56 px     │                      │                                      │ toggle ] │
│ collapsed) │                      │                                      │          │
└────────────┴──────────────────────┴──────────────────────────────────────┴──────────┘
```

- 900–1199 px: navigation collapses to icons; details panel becomes an overlay.
- < 900 px (mobile): separate layout, not a squeezed desktop. Stack navigation: Inbox list →
  conversation (push), back button top-left, bottom action bar with Antwoorden, Notitie,
  Status and a "Meer" sheet. Navigation lives in a sheet from the top-left mailbox switcher.
  Touch targets ≥ 44 px, list rows 56 px with two lines.

## 7. Motion and accessibility

- Durations 100–160 ms, ease-out, only for opacity and small translations (menus, toasts,
  panel toggle). No motion on list updates. `prefers-reduced-motion: reduce` removes all
  transforms and skeleton shimmer.
- Theme follows `prefers-color-scheme` by default, overridable per user (Systeem/Licht/Donker).
- Radix primitives for dialogs, menus, popovers, tooltips, tabs; the list is an ARIA `grid`
  with roving tabindex; live regions announce new conversations ("2 nieuwe gesprekken") and
  send results.
- Every icon-only button has an `aria-label` and a tooltip with the shortcut.
- Target: WCAG 2.2 AA; axe checks run in Playwright on the core screens in both themes.

## 8. Copy (Dutch UI)

Tone: plain, short, informal "je", no exclamation marks, no emoji, no apologies unless we
caused the problem. Say what happened and what the user can do.

| Situation | Copy |
|---|---|
| Empty list | "Geen open gesprekken in Support." · link "Alle gesprekken tonen" |
| Empty search | "Niets gevonden voor ‘factuur 2024’ in Support." · "Zoeken in alle mailboxen" |
| Send failed (permanent) | "Niet verzonden. De mailserver antwoordde: 550 mailbox niet beschikbaar." · "Adres wijzigen" |
| Send failed (temporary) | "Verzenden lukt nog niet. Nieuwe poging om 14:32." · "Nu opnieuw proberen" |
| Uncertain send | "Onbekend of dit bericht is verzonden. Controleer de map Verzonden voordat je het opnieuw verstuurt." · "Opnieuw versturen" |
| Mailbox sync error | "Verbinding met imap.bedrijf.nl mislukt: wachtwoord geweigerd. Laatst gesynchroniseerd om 14:02." · "Inloggegevens bijwerken" |
| Collision | "Sanne typt een antwoord." / "Ook geopend door Sanne en Joris." |
| Undo toast | "Gesprek gesloten" · "Ongedaan maken" |
| SLA timer | "Eerste reactie over 25 min" / "Eerste reactie 12 min te laat" |
| Remote images blocked | "Externe afbeeldingen zijn geblokkeerd." · "Eenmalig tonen" · "Altijd tonen van dit adres" |
| Phishing warning | "De link toont ‘bank.nl’ maar gaat naar ‘bank-verify.co’." |

Glossary (fixed terms): gesprek (conversation), bericht (message), notitie (internal note),
mailbox, inbox, toewijzen, uitstellen (snooze), herinnering, label, prioriteit, standaardantwoord
(template), handtekening, regel (rule), weergave (saved view), rapportage (dashboard),
contactpersoon, organisatie, team. Status names: Open, Wachtend, Gesloten, Spam.

## 9. Logo and wordmark

Wordmark: `echoo` in lower case, Urbanist Medium (500), tracking -1 %, in ink. Mark: ron's logo
tile, a rounded square (12 px) in the accent with a lower-case "e" in `--accent-ink`. The
favicon (`web/public/favicon.svg`) still carries the earlier two-ring mark and has not been
redrawn.
