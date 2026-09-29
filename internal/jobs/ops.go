package jobs

// RetentionPurge applies the workspace's retention periods.
type RetentionPurge struct{}

func (RetentionPurge) Kind() string { return "retention.purge" }

// UploadsPurge removes composer uploads that were never attached and their files.
type UploadsPurge struct{}

func (UploadsPurge) Kind() string { return "uploads.purge" }
