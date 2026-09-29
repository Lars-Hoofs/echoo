package jobs

// CSATSweep sends the satisfaction survey for conversations that were resolved and whose
// mailbox has waited out its delay.
type CSATSweep struct{}

func (CSATSweep) Kind() string { return "csat.sweep" }
