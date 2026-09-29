package api

import (
	"github.com/go-chi/chi/v5"

	"echoo/internal/contacts"
)

// contactRoutes registers the CRM routes every signed-in user may call; what a user sees
// and changes is decided per request from their mailbox scope and role.
func (s *Server) contactRoutes(r chi.Router) {
	r.Get("/contacts", s.listContacts)
	r.Post("/contacts", s.createContact)
	r.Get("/contacts/export", s.exportContacts)
	r.Get("/contacts/{id}", s.getContact)
	r.Patch("/contacts/{id}", s.patchContact)
	r.Post("/contacts/{id}/merge", s.mergeContact)
	r.Get("/contacts/{id}/conversations", s.contactConversations)
	r.Get("/contacts/{id}/timeline", s.contactTimeline)
	r.Get("/contacts/{id}/notes", s.listNotes(contacts.EntityContact))
	r.Post("/contacts/{id}/notes", s.createNote(contacts.EntityContact))
	r.Patch("/contacts/{id}/notes/{noteId}", s.updateNote(contacts.EntityContact))
	r.Delete("/contacts/{id}/notes/{noteId}", s.deleteNote(contacts.EntityContact))

	r.Get("/organizations", s.listOrganizations)
	r.Post("/organizations", s.createOrganization)
	r.Get("/organizations/{id}", s.getOrganization)
	r.Patch("/organizations/{id}", s.patchOrganization)
	r.Get("/organizations/{id}/conversations", s.organizationConversations)
	r.Get("/organizations/{id}/notes", s.listNotes(contacts.EntityOrganization))
	r.Post("/organizations/{id}/notes", s.createNote(contacts.EntityOrganization))
	r.Patch("/organizations/{id}/notes/{noteId}", s.updateNote(contacts.EntityOrganization))
	r.Delete("/organizations/{id}/notes/{noteId}", s.deleteNote(contacts.EntityOrganization))

	r.Get("/custom-attributes", s.listAttributeDefs)
	r.Get("/conversations/{id}/attributes", s.getConversationAttributes)
	r.Patch("/conversations/{id}/attributes", s.patchConversationAttributes)

	r.Get("/contact-segments", s.listSegments)
	r.Post("/contact-segments", s.createSegment)
	r.Patch("/contact-segments/{id}", s.updateSegment)
	r.Delete("/contact-segments/{id}", s.deleteSegment)
}

// contactAdminRoutes registers what only admins may do: custom field definitions, imports and
// the GDPR actions.
func (s *Server) contactAdminRoutes(r chi.Router) {
	r.Post("/custom-attributes", s.createAttributeDef)
	r.Patch("/custom-attributes/{id}", s.updateAttributeDef)
	r.Delete("/custom-attributes/{id}", s.deleteAttributeDef)

	r.Post("/contact-imports", s.createContactImport)
	r.Get("/contact-imports/{id}", s.getContactImport)
	r.Get("/contact-imports/{id}/errors", s.downloadContactImportErrors)

	r.Post("/contacts/{id}/data-export", s.requestContactDataExport)
	r.Post("/contacts/{id}/erase", s.eraseContact)
	r.Get("/data-exports/{id}", s.getDataExport)
	r.Get("/data-exports/{id}/download", s.downloadDataExport)
}
