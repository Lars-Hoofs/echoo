package jobs

// ContactImport processes an uploaded contact CSV file.
type ContactImport struct {
	ImportID string `json:"import_id"`
}

func (ContactImport) Kind() string { return "contacts.import" }

// ContactExport builds the GDPR export ZIP of one contact.
type ContactExport struct {
	ExportID string `json:"export_id"`
}

func (ContactExport) Kind() string { return "contacts.export" }

// ContactsPurge removes expired GDPR exports and old import files.
type ContactsPurge struct{}

func (ContactsPurge) Kind() string { return "contacts.purge" }
