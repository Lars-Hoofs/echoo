package audit

const (
	RetentionChanged = "settings.retention_changed"
	RetentionPurged  = "retention.purged"
	UploadsPurged    = "uploads.purged"
	KeysRotated      = "keys.rotated"
	BlobsDeleted     = "blobs.orphans_deleted"
	// AuditPurged is written by the audit_log_purge SQL function itself, before it deletes.
	AuditPurged = "audit.purged"
)
