// Package jobs defines the arguments of background jobs. Workers live next to the code they
// run; keeping the argument types here lets packages enqueue jobs without importing workers.
package jobs

// ParseRaw parses a stored raw message, threads it and stores it as a conversation message.
type ParseRaw struct {
	RawMessageID string `json:"raw_message_id"`
}

func (ParseRaw) Kind() string { return "mail.parse" }

// SendOutbound delivers one outbound message over SMTP (ADR 0005).
type SendOutbound struct {
	MessageID string `json:"message_id"`
}

func (SendOutbound) Kind() string { return "mail.send" }

// Triggers of EvaluateRules.
const (
	TriggerConversationCreated = "conversation_created"
	TriggerMessageReceived     = "message_received"
	TriggerConversationUpdated = "conversation_updated"
	TriggerSLAAtRisk           = "sla_at_risk"
	TriggerSLABreached         = "sla_breached"
)

// EvaluateRules runs the automation rules of one trigger on one conversation. MessageID is the
// inbound message that caused a conversation_created or message_received trigger.
type EvaluateRules struct {
	ConversationID string `json:"conversation_id"`
	Trigger        string `json:"trigger"`
	MessageID      string `json:"message_id,omitempty"`
}

func (EvaluateRules) Kind() string { return "rules.evaluate" }

// AutomationTick runs the minute-by-minute automation checks: SLA states, rules that wait for
// a silent customer, retries of auto-assignment and auto-resolve.
type AutomationTick struct{}

func (AutomationTick) Kind() string { return "automation.tick" }

// AutomationPurge removes rule runs and auto-reply claims past their retention.
type AutomationPurge struct{}

func (AutomationPurge) Kind() string { return "automation.purge" }
