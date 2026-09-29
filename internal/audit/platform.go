package audit

const (
	APITokenCreated       = "api_token.created"
	APITokenRevoked       = "api_token.revoked"
	WebhookCreated        = "webhook.created"
	WebhookUpdated        = "webhook.updated"
	WebhookDeleted        = "webhook.deleted"
	WebhookTested         = "webhook.tested"
	WebhookDeliveryResent = "webhook.delivery_resent"
	WebhookAutoDisabled   = "webhook.auto_disabled"
	JobRetried            = "job.retried"
	RawMessageRetried     = "raw_message.retried"
	AuditExported         = "audit.exported"
)
