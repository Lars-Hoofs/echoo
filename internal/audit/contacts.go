package audit

const (
	ContactCreated          = "contact.created"
	ContactUpdated          = "contact.updated"
	ContactMerged           = "contact.merged"
	ContactErased           = "contact.erased"
	ContactEraseIncomplete  = "contact.erase_incomplete"
	ContactExported         = "contact.exported"
	ContactDataExportMade   = "contact.data_export_requested"
	ContactDataExportLoaded = "contact.data_export_downloaded"
	ContactImportStarted    = "contact.import_started"
	OrganizationCreated     = "organization.created"
	OrganizationUpdated     = "organization.updated"
	AttributeCreated        = "custom_attribute.created"
	AttributeUpdated        = "custom_attribute.updated"
	AttributeDeleted        = "custom_attribute.deleted"
	SegmentCreated          = "contact_segment.created"
	SegmentUpdated          = "contact_segment.updated"
	SegmentDeleted          = "contact_segment.deleted"
)
