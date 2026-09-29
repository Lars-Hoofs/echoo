# 7. Server-Sent Events carrying IDs only

Status: proposed

## Context
Realtime updates, presence and typing indicators are needed; authorization must stay in one place.

## Decision
One SSE stream per tab. Events contain type, ID and version, never content. Clients refetch
through the normal API. Presence/typing are in-memory with a TTL, fanned out via LISTEN/NOTIFY.

## Consequences
No data leak through the stream, simple reconnect semantics (refetch on gap), no WebSocket
infrastructure. Slightly more API requests on busy inboxes, which TanStack Query deduplicates.
