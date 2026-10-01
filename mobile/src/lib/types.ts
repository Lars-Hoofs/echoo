// API shapes the app uses, as in web/src/lib/inbox.ts, actions.ts and composer.ts.

export type ConversationStatus = 'open' | 'waiting' | 'closed' | 'spam'
export type ListStatus = ConversationStatus | 'snoozed'
export type Priority = 'none' | 'low' | 'normal' | 'high' | 'urgent'
export type InboxView = 'mine' | 'unassigned' | 'all'
export type OutboundStatus = 'queued' | 'sending' | 'retry' | 'sent' | 'failed' | 'uncertain' | 'bounced' | 'cancelled'

export interface Ref {
  id: string
  name: string
}

export interface Address {
  name: string
  address: string
}

export interface User {
  id: string
  email: string
  name: string
  role: string
  availability: 'online' | 'busy' | 'offline'
}

export interface Me {
  user: User
  permissions: string[]
  csrf_token: string
  must_change_password: boolean
  mfa_enrollment_required: boolean
}

export interface InboxSummary {
  counts: { mine: number; unassigned: number; all: number; unread_mine: number }
}

export interface Sla {
  state: 'none' | 'ok' | 'at_risk' | 'breached'
  first_response_due_at: string | null
  first_response_met_at: string | null
  resolution_due_at: string | null
}

export interface ConversationListItem {
  id: string
  number: number
  subject: string
  status: ConversationStatus
  priority: Priority
  version: number
  snoozed_until: string | null
  labels: { id: string; name: string; color_token: string }[]
  preview: string
  last_message_at: string
  message_count: number
  has_attachments: boolean
  last_direction: 'in' | 'out' | null
  mailbox: Ref
  contact: { id: string; name: string; email: string } | null
  assignee: Ref | null
  team: Ref | null
  sla: Sla | null
  unread: boolean
}

export interface ConversationPage {
  conversations: ConversationListItem[]
  next_cursor: string | null
}

export interface Attachment {
  id: string
  filename: string
  size: number
  download_url: string
  dangerous: boolean
  scan_status: 'not_scanned' | 'clean' | 'infected' | 'error'
}

export interface Message {
  id: string
  kind: 'email' | 'note' | 'system'
  direction: 'in' | 'out' | null
  from: Address
  to: Address[]
  cc: Address[]
  subject: string
  body_text: string
  sent_at: string | null
  received_at: string | null
  author: Ref | null
  attachments: Attachment[]
  outbound_status: OutboundStatus | null
  outbound_error: string | null
  has_html: boolean
  render_url: string
  blocked_images: number
  phishing_warnings: { code: string; detail?: string }[]
}

export interface ConversationDetail {
  conversation: ConversationListItem & { created_at: string; can_write: boolean }
  messages: Message[]
  contact: { id: string; name: string; email: string; organization: Ref | null; conversation_count: number } | null
}

export interface TimelineEvent {
  id: string
  type: string
  created_at: string
  actor: Ref | null
  user: Ref | null
  data: Record<string, unknown>
}

export interface AppNotification {
  id: string
  kind: 'mention' | 'assigned' | 'reply' | 'sla' | 'csat'
  conversation_id: string
  conversation_number: number
  conversation_subject: string
  actor: Ref | null
  created_at: string
  read_at: string | null
}

export interface ReplyDefaults {
  to: Address[]
  cc: Address[]
  subject: string
  from: Address
  send_delay_seconds: number
}

export interface Template {
  id: string
  name: string
  shortcode: string
  body_html: string
}

export interface SearchHit {
  conversation: ConversationListItem
  snippet: string
}

export interface PushPrefs {
  mentions: boolean
  assignments: boolean
  replies: boolean
  sla: boolean
}

export interface PushDevice {
  id: string
  kind: 'webpush' | 'apns' | 'fcm'
  label: string
  created_at: string
  last_push_at: string | null
  current: boolean
}
