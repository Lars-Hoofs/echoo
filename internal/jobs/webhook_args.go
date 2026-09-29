package jobs

// WebhookDeliver sends one webhook delivery; the payload is built from the database when the
// job runs, not when it is enqueued.
type WebhookDeliver struct {
	DeliveryID string `json:"delivery_id"`
}

func (WebhookDeliver) Kind() string { return "webhook.deliver" }

// WebhookFanout turns pending outbox events into deliveries, one per subscribed webhook.
type WebhookFanout struct{}

func (WebhookFanout) Kind() string { return "webhook.fanout" }

// WebhookPurge removes delivery logs and processed outbox events past their retention.
type WebhookPurge struct{}

func (WebhookPurge) Kind() string { return "webhook.purge" }
