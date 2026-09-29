package jobs

// CampaignTick advances every campaign that is due or running: it resolves recipients, hands
// the next paced batch to the send queue and records how earlier messages ended.
type CampaignTick struct{}

func (CampaignTick) Kind() string { return "campaigns.tick" }
