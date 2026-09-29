package jobs

// WakeSnoozed clears snoozed_until on conversations whose snooze time has passed.
type WakeSnoozed struct{}

func (WakeSnoozed) Kind() string { return "inbox.wake_snoozed" }
