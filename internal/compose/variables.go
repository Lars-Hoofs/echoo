package compose

import (
	"html"
	"regexp"
	"strings"
)

// Variables is the fixed set a template may use; there is no expression language.
type Variables struct {
	ContactName        string
	ContactEmail       string
	AgentName          string
	ConversationNumber string
	MailboxName        string
}

var variablePattern = regexp.MustCompile(`\{\{\s*([a-z_]+\.[a-z_]+)\s*\}\}`)

// KnownVariables lists the names accepted in templates.
var KnownVariables = []string{
	"contact.name", "contact.first_name", "contact.email",
	"agent.name", "agent.first_name",
	"conversation.number", "mailbox.name",
}

func firstName(full string) string {
	fields := strings.Fields(full)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func (v Variables) value(name string) (string, bool) {
	switch name {
	case "contact.name":
		if v.ContactName == "" {
			return v.ContactEmail, true
		}
		return v.ContactName, true
	case "contact.first_name":
		return firstName(v.ContactName), true
	case "contact.email":
		return v.ContactEmail, true
	case "agent.name":
		return v.AgentName, true
	case "agent.first_name":
		return firstName(v.AgentName), true
	case "conversation.number":
		return v.ConversationNumber, true
	case "mailbox.name":
		return v.MailboxName, true
	}
	return "", false
}

// Render substitutes variables in HTML, escaping each value. A variable that is unknown, or
// known but empty for this conversation, stays visible as written and is listed in unresolved,
// so the agent notices it before sending.
func (v Variables) Render(text string) (out string, unresolved []string) {
	return v.render(text, html.EscapeString)
}

// RenderText is Render for plain text such as a subject line: values are inserted as they are.
func (v Variables) RenderText(text string) (out string, unresolved []string) {
	return v.render(text, func(s string) string { return s })
}

func (v Variables) render(text string, escape func(string) string) (out string, unresolved []string) {
	seen := map[string]bool{}
	out = variablePattern.ReplaceAllStringFunc(text, func(match string) string {
		name := variablePattern.FindStringSubmatch(match)[1]
		val, known := v.value(name)
		if !known || val == "" {
			if !seen[name] {
				seen[name] = true
				unresolved = append(unresolved, name)
			}
			return match
		}
		return escape(val)
	})
	return out, unresolved
}
