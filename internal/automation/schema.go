// Package automation runs the workspace rules: conditions and actions stored as JSON, the
// engine that evaluates them, macros, auto-assignment, SLA tracking and auto-resolve.
package automation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/inbox"
	"echoo/internal/jobs"
)

// Triggers a rule can listen to. CustomerIdle is time based and checked by a periodic job.
const (
	TriggerCustomerIdle = "customer_idle"
)

// Triggers lists every valid rule trigger.
var Triggers = []string{
	jobs.TriggerConversationCreated, jobs.TriggerMessageReceived, jobs.TriggerConversationUpdated,
	jobs.TriggerSLAAtRisk, jobs.TriggerSLABreached, TriggerCustomerIdle,
}

const (
	maxConditions  = 20
	maxGroupDepth  = 2
	maxActions     = 10
	maxTextRunes   = 200
	maxReplyRunes  = 5000
	maxListValues  = 20
	maxIdleHours   = 720
	matchAll       = "all"
	matchAny       = "any"
	opIs           = "is"
	opHasAny       = "has_any"
	opEquals       = "equals"
	opContains     = "contains"
	opStartsWith   = "starts_with"
	opEndsWith     = "ends_with"
	fieldLabelName = "label"
)

// ValidationError lists what is wrong with a rule or macro, keyed by a JSON path such as
// "conditions.items.0.value".
type ValidationError map[string]string

func (v ValidationError) Error() string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + ": " + v[k]
	}
	return "invalid: " + strings.Join(parts, "; ")
}

type valueKind int

const (
	kindText valueKind = iota
	kindIDs
	kindBool
)

type fieldSpec struct {
	kind valueKind
	ops  []string
	// values restricts kindIDs to a closed list; nil means UUIDs.
	values []string
}

var textOps = []string{opEquals, opContains, opStartsWith, opEndsWith}

var fieldSpecs = map[string]fieldSpec{
	"mailbox":               {kindIDs, []string{opIs}, nil},
	"from_address":          {kindText, textOps, nil},
	"from_domain":           {kindText, textOps, nil},
	"subject":               {kindText, textOps, nil},
	"body":                  {kindText, textOps, nil},
	"organization":          {kindText, textOps, nil},
	"has_attachment":        {kindBool, []string{opIs}, nil},
	"status":                {kindIDs, []string{opIs}, []string{inbox.StatusOpen, inbox.StatusWaiting, inbox.StatusClosed, inbox.StatusSpam}},
	"priority":              {kindIDs, []string{opIs}, []string{"none", "low", "normal", "high", "urgent"}},
	fieldLabelName:          {kindIDs, []string{opHasAny}, nil},
	"has_assignee":          {kindBool, []string{opIs}, nil},
	"has_team":              {kindBool, []string{opIs}, nil},
	"auto_submitted":        {kindBool, []string{opIs}, nil},
	"within_business_hours": {kindBool, []string{opIs}, nil},
}

// Node is one entry of a condition group: either a group (Match and Items) or a condition
// (Field, Op, Value). Value is text, a list of ids or a boolean depending on the field.
type Node struct {
	Match  string          `json:"match,omitempty"`
	Items  []Node          `json:"items,omitempty"`
	Field  string          `json:"field,omitempty"`
	Op     string          `json:"op,omitempty"`
	Value  json.RawMessage `json:"value,omitempty"`
	Negate bool            `json:"negate,omitempty"`

	text string
	ids  []string
	flag bool
}

func (n *Node) isGroup() bool { return n.Match != "" || n.Items != nil }

// Conditions is a validated condition tree; the zero value matches everything.
type Conditions struct{ root Node }

// MarshalJSON writes the normalized form that is stored.
func (c Conditions) MarshalJSON() ([]byte, error) {
	root := struct {
		Match string `json:"match"`
		Items []Node `json:"items"`
	}{Match: c.root.Match, Items: c.root.Items}
	if root.Match == "" {
		root.Match = matchAll
	}
	if root.Items == nil {
		root.Items = []Node{}
	}
	return json.Marshal(root)
}

// ParseConditions decodes raw with unknown fields rejected and checks every condition against
// the closed field table. An empty document means no conditions.
func ParseConditions(raw []byte) (Conditions, error) {
	var n Node
	if err := decodeStrict(raw, &n); err != nil {
		return Conditions{}, ValidationError{"conditions": err.Error()}
	}
	if !n.isGroup() || n.Field != "" {
		return Conditions{}, ValidationError{"conditions": "must be a group with match and items"}
	}
	if n.Items == nil {
		n.Items = []Node{}
	}
	errs := ValidationError{}
	count := 0
	n.validate("conditions", 1, &count, errs)
	if len(errs) > 0 {
		return Conditions{}, errs
	}
	return Conditions{root: n}, nil
}

func (n *Node) validate(path string, depth int, count *int, errs ValidationError) {
	if n.isGroup() {
		n.validateGroup(path, depth, count, errs)
		return
	}
	*count++
	if *count > maxConditions {
		errs[path] = "too_many"
		return
	}
	spec, ok := fieldSpecs[n.Field]
	if !ok {
		errs[path+".field"] = "unknown"
		return
	}
	if !slices.Contains(spec.ops, n.Op) {
		errs[path+".op"] = "invalid"
		return
	}
	n.validateValue(path+".value", spec, errs)
}

func (n *Node) validateGroup(path string, depth int, count *int, errs ValidationError) {
	if n.Field != "" || n.Op != "" || n.Value != nil || n.Negate {
		errs[path] = "group_has_condition_fields"
		return
	}
	if n.Match != matchAll && n.Match != matchAny {
		errs[path+".match"] = "invalid"
		return
	}
	if depth > maxGroupDepth {
		errs[path] = "too_deep"
		return
	}
	if depth > 1 && len(n.Items) == 0 {
		errs[path+".items"] = "required"
		return
	}
	for i := range n.Items {
		n.Items[i].validate(fmt.Sprintf("%s.items.%d", path, i), depth+1, count, errs)
	}
}

func (n *Node) validateValue(path string, spec fieldSpec, errs ValidationError) {
	defer func() {
		if _, bad := errs[path]; bad {
			return
		}
		// Store what was understood, not what was typed: lower-cased text, canonical ids.
		switch spec.kind {
		case kindText:
			n.Value, _ = json.Marshal(n.text)
		case kindIDs:
			n.Value, _ = json.Marshal(n.ids)
		case kindBool:
			n.Value, _ = json.Marshal(n.flag)
		}
	}()
	switch spec.kind {
	case kindText:
		var s string
		if err := decodeStrict(n.Value, &s); err != nil {
			errs[path] = "invalid"
			return
		}
		n.text = strings.ToLower(strings.TrimSpace(s))
		if n.Field == "from_domain" {
			n.text = strings.TrimPrefix(n.text, "@")
		}
		if n.text == "" || utf8.RuneCountInString(n.text) > maxTextRunes || hasControl(n.text) {
			errs[path] = "invalid"
		}
	case kindBool:
		if err := decodeStrict(n.Value, &n.flag); err != nil {
			errs[path] = "invalid"
		}
	case kindIDs:
		if err := decodeStrict(n.Value, &n.ids); err != nil || len(n.ids) == 0 || len(n.ids) > maxListValues {
			errs[path] = "invalid"
			return
		}
		for i, id := range n.ids {
			if spec.values != nil {
				if !slices.Contains(spec.values, id) {
					errs[path] = "invalid"
				}
				continue
			}
			u, ok := parseID(id)
			if !ok {
				errs[path] = "invalid"
				return
			}
			n.ids[i] = u.String()
		}
	}
}

// Action types. Each one accepts exactly the fields listed in actionSpecs.
const (
	ActionAssignAgent      = "assign_agent"
	ActionAssignTeam       = "assign_team"
	ActionAssignRoundRobin = "assign_round_robin"
	ActionSetPriority      = "set_priority"
	ActionAddLabel         = "add_label"
	ActionRemoveLabel      = "remove_label"
	ActionSetStatus        = "set_status"
	ActionSnooze           = "snooze"
	ActionApplySLA         = "apply_sla"
	ActionAutoReply        = "auto_reply"
	ActionAddNote          = "add_note"
	ActionSendWebhook      = "send_webhook"
	ActionMarkSpam         = "mark_spam"
)

// Action is one step of a rule or macro.
type Action struct {
	Type       string `json:"type"`
	UserID     string `json:"user_id,omitempty"`
	TeamID     string `json:"team_id,omitempty"`
	Priority   string `json:"priority,omitempty"`
	LabelID    string `json:"label_id,omitempty"`
	Status     string `json:"status,omitempty"`
	Hours      int    `json:"hours,omitempty"`
	PolicyID   string `json:"policy_id,omitempty"`
	TemplateID string `json:"template_id,omitempty"`
	Subject    string `json:"subject,omitempty"`
	Text       string `json:"text,omitempty"`
}

type actionSpec struct{ required, optional []string }

var actionSpecs = map[string]actionSpec{
	ActionAssignAgent:      {required: []string{"user_id"}},
	ActionAssignTeam:       {required: []string{"team_id"}},
	ActionAssignRoundRobin: {optional: []string{"team_id"}},
	ActionSetPriority:      {required: []string{"priority"}},
	ActionAddLabel:         {required: []string{"label_id"}},
	ActionRemoveLabel:      {required: []string{"label_id"}},
	ActionSetStatus:        {required: []string{"status"}},
	ActionSnooze:           {required: []string{"hours"}},
	ActionApplySLA:         {required: []string{"policy_id"}},
	ActionAddNote:          {required: []string{"text"}},
	ActionSendWebhook:      {},
	ActionMarkSpam:         {},
	// Either a canned response or inline text, never both.
	ActionAutoReply: {optional: []string{"template_id", "text", "subject"}},
}

func (a Action) present() map[string]bool {
	m := map[string]bool{}
	set := func(name string, ok bool) {
		if ok {
			m[name] = true
		}
	}
	set("user_id", a.UserID != "")
	set("team_id", a.TeamID != "")
	set("priority", a.Priority != "")
	set("label_id", a.LabelID != "")
	set("status", a.Status != "")
	set("hours", a.Hours != 0)
	set("policy_id", a.PolicyID != "")
	set("template_id", a.TemplateID != "")
	set("subject", a.Subject != "")
	set("text", a.Text != "")
	return m
}

const maxSnoozeHours = 24 * 365

// ParseActions decodes a list of actions, rejects unknown fields and checks every action.
// allowAutoReply is false for macros: a person sends replies from the composer.
func ParseActions(raw []byte, allowAutoReply bool) ([]Action, error) {
	var list []Action
	if err := decodeStrict(raw, &list); err != nil {
		return nil, ValidationError{"actions": err.Error()}
	}
	errs := ValidationError{}
	switch {
	case len(list) == 0:
		errs["actions"] = "required"
	case len(list) > maxActions:
		errs["actions"] = "too_many"
	}
	for i := range list {
		list[i].validate(fmt.Sprintf("actions.%d", i), allowAutoReply, errs)
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return list, nil
}

func (a *Action) validate(path string, allowAutoReply bool, errs ValidationError) {
	spec, ok := actionSpecs[a.Type]
	if !ok || (a.Type == ActionAutoReply && !allowAutoReply) {
		errs[path+".type"] = "unknown"
		return
	}
	present := a.present()
	for _, f := range spec.required {
		if !present[f] {
			errs[path+"."+f] = "required"
		}
	}
	for f := range present {
		if !slices.Contains(spec.required, f) && !slices.Contains(spec.optional, f) {
			errs[path+"."+f] = "not_allowed"
		}
	}
	if len(errs) > 0 {
		return
	}
	a.checkValues(path, errs)
}

func (a *Action) checkValues(path string, errs ValidationError) {
	for _, f := range []struct {
		name string
		id   *string
	}{{"user_id", &a.UserID}, {"team_id", &a.TeamID}, {"label_id", &a.LabelID}, {"policy_id", &a.PolicyID}, {"template_id", &a.TemplateID}} {
		if *f.id == "" {
			continue
		}
		u, ok := parseID(*f.id)
		if !ok {
			errs[path+"."+f.name] = "invalid"
			continue
		}
		*f.id = u.String()
	}
	if a.Priority != "" && !inbox.ValidPriority(a.Priority) {
		errs[path+".priority"] = "invalid"
	}
	if a.Status != "" && (!inbox.ValidStatus(a.Status) || a.Status == inbox.StatusSpam) {
		errs[path+".status"] = "invalid"
	}
	if a.Hours != 0 && (a.Hours < 1 || a.Hours > maxSnoozeHours) {
		errs[path+".hours"] = "invalid"
	}
	if utf8.RuneCountInString(a.Subject) > maxTextRunes || hasControl(a.Subject) {
		errs[path+".subject"] = "invalid"
	}
	if utf8.RuneCountInString(a.Text) > maxReplyRunes || strings.TrimSpace(a.Text) == "" && a.Text != "" {
		errs[path+".text"] = "invalid"
	}
	if a.Type == ActionAutoReply {
		if (a.TemplateID == "") == (a.Text == "") {
			errs[path+".text"] = "template_or_text"
		}
		if a.TemplateID != "" && a.Subject != "" {
			errs[path+".subject"] = "not_allowed"
		}
	}
}

// RuleDef is the writable part of a rule.
type RuleDef struct {
	Name           string
	MailboxID      pgtype.UUID
	Trigger        string
	IdleHours      int
	Conditions     json.RawMessage
	Actions        json.RawMessage
	StopProcessing bool
	Enabled        bool
}

// ValidateRule checks the whole rule and returns it with the normalized JSON to store.
func ValidateRule(in RuleDef) (RuleDef, error) {
	errs := ValidationError{}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 100 || hasControl(in.Name) {
		errs["name"] = "invalid"
	}
	if !slices.Contains(Triggers, in.Trigger) {
		errs["trigger"] = "invalid"
	}
	switch {
	case in.Trigger == TriggerCustomerIdle && (in.IdleHours < 1 || in.IdleHours > maxIdleHours):
		errs["idle_hours"] = "invalid"
	case in.Trigger != TriggerCustomerIdle && in.IdleHours != 0:
		errs["idle_hours"] = "not_allowed"
	}
	conds, err := ParseConditions(orEmpty(in.Conditions, `{"match":"all","items":[]}`))
	mergeErrors(errs, err)
	actions, err := ParseActions(orEmpty(in.Actions, `[]`), true)
	mergeErrors(errs, err)
	if len(errs) > 0 {
		return RuleDef{}, errs
	}
	// Marshaling values this package built cannot fail.
	in.Conditions, _ = json.Marshal(conds)
	in.Actions, _ = json.Marshal(actions)
	return in, nil
}

func mergeErrors(dst ValidationError, err error) {
	var ve ValidationError
	if errors.As(err, &ve) {
		for k, v := range ve {
			dst[k] = v
		}
	}
}

func orEmpty(raw json.RawMessage, fallback string) []byte {
	if len(bytes.TrimSpace(raw)) == 0 {
		return []byte(fallback)
	}
	return raw
}

// decodeStrict decodes exactly one JSON value and rejects unknown fields.
func decodeStrict(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errors.New("invalid")
	}
	if dec.More() {
		return errors.New("invalid")
	}
	return nil
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

func parseID(s string) (pgtype.UUID, bool) {
	var id pgtype.UUID
	if err := id.Scan(s); err != nil {
		return id, false
	}
	return id, true
}
