package jobs

// SysmailSend delivers one system email (invitation, password reset, notification). Payload is
// the recipient and message encrypted with the keyring: the message holds single-use links, and
// job arguments are readable in the jobs table and the admin job viewer.
type SysmailSend struct {
	Payload []byte `json:"payload"`
}

func (SysmailSend) Kind() string { return "sysmail.send" }

// NotificationMail turns unhandled notifications into system emails for users who opted in.
type NotificationMail struct{}

func (NotificationMail) Kind() string { return "notification.mail" }
