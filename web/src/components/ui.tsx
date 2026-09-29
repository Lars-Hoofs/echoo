import { CircleAlert, ChevronDown, Loader2, TriangleAlert } from 'lucide-react'
import {
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  type SelectHTMLAttributes,
  type TdHTMLAttributes,
  type TextareaHTMLAttributes,
  type ThHTMLAttributes,
  useId,
} from 'react'

import { Reveal } from './Reveal'

/*
 * Shapes follow ron.css (see DESIGN.md sections 4 and 5):
 *   pill (rounded-full)  buttons, inputs, selects, tags, tabs, menu items
 *   rounded-md (12px)    small surfaces: textareas, skeletons, notices
 *   rounded-lg (24px)    cards, dialogs, menus
 * Control heights are 32 (sm), 44 (md, default) and 56 (lg); sm grows to 44 on phones for touch.
 * Colour is ink and neutral. The accent means "the customer waits on us" and appears only where
 * that is the point (variant="accent", the open status, unread counts); red is for errors.
 */

type Variant = 'primary' | 'accent' | 'secondary' | 'danger' | 'ghost'
type Size = 'sm' | 'md' | 'lg'

const variants: Record<Variant, string> = {
  primary: 'btn-primary',
  accent: 'btn-accent',
  secondary: '',
  danger: 'border-transparent bg-danger text-on-danger hover:bg-danger hover:opacity-90',
  ghost: 'border-transparent text-muted hover:text-ink',
}

const sizes: Record<Size, string> = {
  sm: 'btn-s max-md:h-11 max-md:px-4 max-md:text-sm',
  md: '',
  lg: 'btn-l',
}

// The button look for elements that are not a <button>: a router Link or an <a href> styled as one.
export function buttonClass({ variant = 'secondary', size = 'md' }: { variant?: Variant; size?: Size } = {}): string {
  return `btn ${sizes[size]} ${variants[variant]}`
}

export function Button({
  variant = 'secondary',
  size = 'md',
  busy = false,
  className = '',
  children,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; size?: Size; busy?: boolean }) {
  return (
    <button
      type="button"
      {...props}
      disabled={(props.disabled ?? false) || busy}
      aria-busy={busy || undefined}
      className={`${buttonClass({ variant, size })} disabled:cursor-not-allowed disabled:opacity-60 ${className}`}
    >
      {busy && <Loader2 size={16} aria-hidden className="motion-safe:animate-spin" />}
      {children}
    </button>
  )
}

const iconSizes: Record<Size, string> = { sm: 'icon-btn-s max-md:size-11', md: '', lg: 'icon-btn-l' }

// A round, icon-only button. The label is required: it becomes the accessible name and the tooltip.
export function IconButton({
  label,
  size = 'md',
  className = '',
  children,
  ...props
}: Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'aria-label' | 'title'> & { label: string; size?: Size }) {
  return (
    <button
      type="button"
      {...props}
      aria-label={label}
      title={label}
      className={`icon-btn ${iconSizes[size]} disabled:cursor-not-allowed disabled:opacity-60 ${className}`}
    >
      {children}
    </button>
  )
}

const control =
  'w-full border border-line-input bg-transparent text-base text-ink transition-colors placeholder:text-faint hover:border-ink/40 focus-visible:border-ink disabled:cursor-not-allowed disabled:bg-subtle disabled:text-faint aria-invalid:border-danger-text max-md:text-lg'

export function Input({ className = '', ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return <input {...props} className={`h-11 rounded-full px-4 ${control} ${className}`} />
}

export function Textarea({ className = '', ...props }: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea {...props} className={`min-h-24 resize-y rounded-md px-4 py-3 ${control} ${className}`} />
}

// A native select with a custom chevron. className applies to the wrapper (for width); id and aria
// props go to the select itself.
export function Select({ className = '', children, ...props }: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <div className={`relative ${className}`}>
      <select {...props} className={`h-11 appearance-none rounded-full pr-10 pl-4 ${control}`}>
        {children}
      </select>
      <ChevronDown size={16} aria-hidden className="pointer-events-none absolute top-1/2 right-4 -translate-y-1/2 text-faint" />
    </div>
  )
}

// Field renders a labelled control with help and error text wired up for screen readers.
export function Field({
  label,
  help,
  error,
  children,
}: {
  label: string
  help?: ReactNode
  error?: string | undefined
  children: (props: { id: string; 'aria-describedby'?: string; 'aria-invalid'?: true }) => ReactNode
}) {
  const id = useId()
  const describedBy = [help ? `${id}-help` : '', error ? `${id}-error` : ''].filter(Boolean).join(' ')
  return (
    <div className="flex flex-col gap-2">
      <label htmlFor={id} className="t-label">
        {label}
      </label>
      {children({
        id,
        ...(describedBy ? { 'aria-describedby': describedBy } : {}),
        ...(error ? { 'aria-invalid': true as const } : {}),
      })}
      {help && (
        <p id={`${id}-help`} className="t-label">
          {help}
        </p>
      )}
      {error && (
        <p id={`${id}-error`} className="flex items-center gap-1 text-sm text-danger-text">
          <CircleAlert size={14} aria-hidden className="shrink-0" />
          {error}
        </p>
      )}
    </div>
  )
}

export function ErrorNotice({ children }: { children: ReactNode }) {
  return (
    <div role="alert" className="flex items-start gap-3 rounded-md border border-danger-text/30 bg-danger-subtle px-4 py-3 text-base text-ink">
      <TriangleAlert size={20} aria-hidden className="shrink-0 text-danger-text" />
      <div className="min-w-0">{children}</div>
    </div>
  )
}

export function Wordmark({ className = '' }: { className?: string }) {
  return (
    <span className={`font-medium tracking-[-0.01em] text-ink ${className}`} aria-label="Echoo">
      echoo
    </span>
  )
}

// Centered content column for a page. Children are spaced 32px apart.
export function Page({ children }: { children: ReactNode }) {
  return <div className="mx-auto flex w-full max-w-[880px] flex-col gap-8">{children}</div>
}

// The breadcrumb is the small grey line above the title (ron's kicker).
export function PageHeader({
  title,
  description,
  actions,
  breadcrumb,
}: {
  title: string
  description?: string
  actions?: ReactNode
  breadcrumb?: ReactNode
}) {
  return (
    <Reveal>
      <header>
        {breadcrumb && <div className="t-label mb-2">{breadcrumb}</div>}
        <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-3">
          <div className="min-w-0">
            <h1 className="t-h3 text-ink">{title}</h1>
            {description && <p className="t-body mt-2 max-w-xl">{description}</p>}
          </div>
          {actions && <div className="flex max-w-full flex-wrap items-center gap-2">{actions}</div>}
        </div>
      </header>
    </Reveal>
  )
}

// A titled group of cards on a page.
export function Section({ title, description, children }: { title: string; description?: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-4">
      <div>
        <h2 className="t-title text-ink">{title}</h2>
        {description && <p className="t-body mt-1">{description}</p>}
      </div>
      {children}
    </section>
  )
}

// A ruled panel for grouped settings. Use flush for content that brings its own padding
// (SettingRow lists, tables); the header and footer are optional.
export function Card({
  title,
  description,
  icon,
  actions,
  footer,
  flush = false,
  children,
}: {
  title?: string
  description?: ReactNode
  icon?: ReactNode
  actions?: ReactNode
  footer?: ReactNode
  flush?: boolean
  children: ReactNode
}) {
  return (
    <section className="card card-line overflow-hidden p-0">
      {title && (
        <div className="flex items-start justify-between gap-4 border-b border-line px-4 py-4 sm:px-6">
          <div className="flex min-w-0 items-start gap-3">
            {icon && (
              <span aria-hidden className="mt-0.5 shrink-0 text-muted">
                {icon}
              </span>
            )}
            <div className="min-w-0">
              <h2 className="t-title text-ink">{title}</h2>
              {description && <p className="t-body mt-1">{description}</p>}
            </div>
          </div>
          {actions && <div className="flex max-w-full flex-wrap items-center gap-2">{actions}</div>}
        </div>
      )}
      <div className={flush ? '' : 'p-4 sm:p-6'}>{children}</div>
      {footer && <div className="flex flex-wrap items-center justify-end gap-2 border-t border-line px-4 py-4 sm:px-6">{footer}</div>}
    </section>
  )
}

// A settings row: label and help on the left, control on the right. Meant for flush cards.
export function SettingRow({ title, help, children }: { title: string; help?: ReactNode; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-3 border-b border-line px-4 py-4 last:border-b-0 sm:flex-row sm:items-center sm:justify-between sm:gap-6 sm:px-6">
      <div className="max-w-md min-w-0">
        <div className="text-base text-ink">{title}</div>
        {help && <div className="t-label mt-1">{help}</div>}
      </div>
      <div className="shrink-0">{children}</div>
    </div>
  )
}

type BadgeTone = 'neutral' | 'accent' | 'signal' | 'danger'

const badgeTones: Record<BadgeTone, string> = {
  neutral: '',
  accent: 'tag-accent',
  signal: 'bg-signal text-on-signal',
  danger: 'tag-alert',
}

// Status is always conveyed by the text; the icon and tone only reinforce it. accent means the
// customer waits on us, danger is an error or a breach.
export function Badge({
  tone = 'neutral',
  icon,
  dot = false,
  children,
}: {
  tone?: BadgeTone
  icon?: ReactNode
  dot?: boolean
  children: ReactNode
}) {
  return (
    <span className={`tag gap-1 ${badgeTones[tone]}`}>
      {dot && <span aria-hidden className="size-1.5 shrink-0 rounded-full bg-current opacity-60" />}
      {icon && (
        <span aria-hidden className="inline-flex shrink-0">
          {icon}
        </span>
      )}
      {children}
    </span>
  )
}

// A switch on a plain button with role=switch. Name it with label (or aria-labelledby via props).
// On is ink, not accent: a setting being on is not "the customer waits".
export function Switch({
  checked,
  onChange,
  label,
  disabled = false,
}: {
  checked: boolean
  onChange: (checked: boolean) => void
  label: string
  disabled?: boolean
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className="relative h-6 w-11 shrink-0 rounded-full border border-line-input bg-subtle transition-colors after:absolute after:-inset-3 after:content-[''] disabled:cursor-not-allowed disabled:opacity-60 aria-checked:border-transparent aria-checked:bg-ink"
    >
      <span className="absolute top-1/2 left-1 size-4 -translate-y-1/2 rounded-full bg-muted transition-transform in-aria-checked:translate-x-5 in-aria-checked:bg-surface" />
    </button>
  )
}

export function Kbd({ children }: { children: ReactNode }) {
  return <kbd className="tag min-w-6 justify-center border border-line-strong bg-transparent px-2 font-mono">{children}</kbd>
}

// Segmented choice built on native radio inputs, so arrow keys and screen readers work as expected.
// Looks like ron's tabs: a pill track, the chosen option solid ink.
export function Segmented<T extends string>({
  label,
  value,
  options,
  onChange,
}: {
  label: string
  value: T
  options: { value: T; label: string }[]
  onChange: (value: T) => void
}) {
  const name = useId()
  return (
    <fieldset className="tabs max-w-full overflow-x-auto">
      <legend className="sr-only">{label}</legend>
      {options.map((o) => (
        <label
          key={o.value}
          className="tab inline-flex items-center hover:text-ink has-checked:bg-ink has-checked:text-on-ink has-checked:hover:text-on-ink has-focus-visible:outline-2 has-focus-visible:outline-offset-2 has-focus-visible:outline-ink max-md:h-11"
        >
          <input
            type="radio"
            name={name}
            value={o.value}
            checked={value === o.value}
            onChange={() => onChange(o.value)}
            className="sr-only"
          />
          {o.label}
        </label>
      ))}
    </fieldset>
  )
}

// Tables: put them in a flush Card. Th/Td accept className for responsive hiding.
export function Table({ children }: { children: ReactNode }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full border-collapse text-left text-base">{children}</table>
    </div>
  )
}

export function THead({ children }: { children: ReactNode }) {
  return (
    <thead>
      <tr className="border-b border-line">{children}</tr>
    </thead>
  )
}

export function TBody({ children }: { children: ReactNode }) {
  return <tbody className="divide-y divide-line">{children}</tbody>
}

export function Tr({ children }: { children: ReactNode }) {
  return <tr className="transition-colors hover:bg-subtle/60">{children}</tr>
}

export function Th({ numeric = false, className = '', ...props }: ThHTMLAttributes<HTMLTableCellElement> & { numeric?: boolean }) {
  return (
    <th
      {...props}
      className={`t-label px-4 py-3 font-normal whitespace-nowrap first:pl-4 last:pr-4 sm:first:pl-6 sm:last:pr-6 ${numeric ? 'text-right' : ''} ${className}`}
    />
  )
}

export function Td({ numeric = false, className = '', ...props }: TdHTMLAttributes<HTMLTableCellElement> & { numeric?: boolean }) {
  return (
    <td
      {...props}
      className={`px-4 py-3 align-middle first:pl-4 last:pr-4 sm:first:pl-6 sm:last:pr-6 ${numeric ? 'text-right tabular-nums' : ''} ${className}`}
    />
  )
}

export function EmptyState({ icon, title, description, action }: { icon: ReactNode; title: string; description?: string; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-4 px-6 py-12 text-center">
      <span aria-hidden className="flex size-11 items-center justify-center rounded-full bg-subtle text-muted">
        {icon}
      </span>
      <div>
        <p className="t-title text-ink">{title}</p>
        {description && <p className="t-body mt-1">{description}</p>}
      </div>
      {action}
    </div>
  )
}

export function Skeleton({ className = 'h-24' }: { className?: string }) {
  return <div aria-busy="true" className={`rounded-md bg-subtle motion-safe:animate-pulse ${className}`} />
}
