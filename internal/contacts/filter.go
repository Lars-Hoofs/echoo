package contacts

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"echoo/internal/db/dbq"
)

const (
	MatchAll = "all"
	MatchAny = "any"

	FieldName                  = "name"
	FieldEmail                 = "email"
	FieldDomain                = "domain"
	FieldOrganization          = "organization"
	FieldHasOpenConversation   = "has_open_conversation"
	FieldLastActivity          = "last_activity"
	FieldAttribute             = "attribute"
	FieldConversationAttribute = "conversation_attribute"

	maxConditions = 20
	maxValueLen   = 200
)

// Filter is a saved or ad-hoc condition set over contacts. It only ever becomes SQL through
// buildCondition, which binds every value as a parameter.
type Filter struct {
	Match      string      `json:"match"`
	Conditions []Condition `json:"conditions"`
}

type Condition struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	// Key names the custom attribute for the attribute fields.
	Key   string `json:"key,omitempty"`
	Value string `json:"value,omitempty"`
}

var (
	textOps      = []string{"contains", "not_contains", "equals", "not_equals", "starts_with", "is_set", "is_not_set"}
	domainOps    = []string{"contains", "equals", "not_equals"}
	boolOps      = []string{"is_true", "is_false"}
	activityOps  = []string{"before", "on_or_after", "within_days", "older_than_days"}
	listOps      = []string{"equals", "not_equals", "is_set", "is_not_set"}
	numberOps    = []string{"equals", "not_equals", "gt", "lt", "is_set", "is_not_set"}
	dateOps      = []string{"equals", "before", "on_or_after", "is_set", "is_not_set"}
	booleanAttrs = []string{"is_true", "is_false", "is_set", "is_not_set"}
)

// OpsFor lists the operators allowed for an attribute type; the UI uses the same table.
func OpsFor(attributeType string) []string {
	switch attributeType {
	case TypeNumber:
		return numberOps
	case TypeDate:
		return dateOps
	case TypeBoolean:
		return booleanAttrs
	case TypeList:
		return listOps
	default:
		return textOps
	}
}

// ParseFilter decodes and validates a filter against the attribute definitions.
func ParseFilter(raw []byte, defs []dbq.CustomAttributeDef) (Filter, error) {
	var f Filter
	if err := json.Unmarshal(raw, &f); err != nil {
		return Filter{}, fmt.Errorf("invalid filter: %w", err)
	}
	if err := f.Validate(defs); err != nil {
		return Filter{}, err
	}
	return f, nil
}

func (f *Filter) Validate(defs []dbq.CustomAttributeDef) error {
	if f.Match == "" {
		f.Match = MatchAll
	}
	if f.Match != MatchAll && f.Match != MatchAny {
		return fmt.Errorf("match must be %s or %s", MatchAll, MatchAny)
	}
	if len(f.Conditions) > maxConditions {
		return fmt.Errorf("at most %d conditions", maxConditions)
	}
	for i := range f.Conditions {
		if err := f.Conditions[i].validate(defs); err != nil {
			return fmt.Errorf("condition %d: %w", i+1, err)
		}
	}
	return nil
}

func needsValue(op string) bool {
	return !slices.Contains([]string{"is_set", "is_not_set", "is_true", "is_false"}, op)
}

func (c *Condition) validate(defs []dbq.CustomAttributeDef) error {
	var allowed []string
	attrType := ""
	switch c.Field {
	case FieldName, FieldEmail, FieldOrganization:
		allowed = textOps
		if c.Field == FieldEmail {
			allowed = []string{"contains", "not_contains", "equals", "not_equals", "starts_with"}
		}
	case FieldDomain:
		allowed = domainOps
	case FieldHasOpenConversation:
		allowed = boolOps
	case FieldLastActivity:
		allowed = activityOps
	case FieldAttribute, FieldConversationAttribute:
		entity := EntityContact
		if c.Field == FieldConversationAttribute {
			entity = EntityConversation
		}
		def, ok := findDef(defs, entity, c.Key)
		if !ok {
			return fmt.Errorf("unknown attribute %q", c.Key)
		}
		attrType = def.Type
		allowed = OpsFor(def.Type)
	default:
		return fmt.Errorf("unknown field %q", c.Field)
	}
	if c.Field != FieldAttribute && c.Field != FieldConversationAttribute && c.Key != "" {
		return fmt.Errorf("key is only for attribute fields")
	}
	if !slices.Contains(allowed, c.Op) {
		return fmt.Errorf("operator %q is not allowed for %s", c.Op, c.Field)
	}
	if !needsValue(c.Op) {
		c.Value = ""
		return nil
	}
	c.Value = strings.TrimSpace(c.Value)
	if c.Value == "" || len(c.Value) > maxValueLen {
		return fmt.Errorf("a value of 1 to %d characters is required", maxValueLen)
	}
	return c.validateValue(attrType)
}

func (c Condition) validateValue(attrType string) error {
	switch {
	case c.Field == FieldLastActivity && (c.Op == "before" || c.Op == "on_or_after"):
		return checkDate(c.Value)
	case c.Field == FieldLastActivity:
		return checkDays(c.Value)
	case attrType == TypeNumber:
		if _, err := strconv.ParseFloat(c.Value, 64); err != nil {
			return fmt.Errorf("expected a number")
		}
	case attrType == TypeDate:
		return checkDate(c.Value)
	}
	return nil
}

func checkDate(v string) error {
	if _, err := time.Parse(time.DateOnly, v); err != nil {
		return fmt.Errorf("expected a date as YYYY-MM-DD")
	}
	return nil
}

func checkDays(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > 36500 {
		return fmt.Errorf("expected a number of days")
	}
	return nil
}

func findDef(defs []dbq.CustomAttributeDef, entity, key string) (dbq.CustomAttributeDef, bool) {
	for _, d := range defs {
		if d.Entity == entity && d.Key == key {
			return d, true
		}
	}
	return dbq.CustomAttributeDef{}, false
}

// sqlArgs collects bound parameters and hands out their placeholders.
type sqlArgs struct{ values []any }

func (a *sqlArgs) add(v any) string {
	a.values = append(a.values, v)
	return "$" + strconv.Itoa(len(a.values))
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// EscapeLike makes s safe to embed in a LIKE or ILIKE pattern.
func EscapeLike(s string) string { return likeEscaper.Replace(s) }

// textCondition compares a text expression. Negative operators negate the positive one.
func textCondition(expr, op, value string, a *sqlArgs) string {
	switch op {
	case "contains":
		return expr + " ILIKE '%' || " + a.add(EscapeLike(value)) + " || '%'"
	case "not_contains":
		return "NOT (" + textCondition(expr, "contains", value, a) + ")"
	case "equals":
		return "lower(" + expr + ") = lower(" + a.add(value) + "::text)"
	case "not_equals":
		return "NOT (" + textCondition(expr, "equals", value, a) + ")"
	case "starts_with":
		return expr + " ILIKE " + a.add(EscapeLike(value)) + " || '%'"
	case "is_set":
		return expr + " <> ''"
	default:
		return expr + " = ''"
	}
}

// multiRowCondition applies a text condition to any of a contact's addresses; negative
// operators mean that no address matches.
func multiRowCondition(expr, op, value string, a *sqlArgs) string {
	positive := map[string]string{"not_contains": "contains", "not_equals": "equals"}
	if p, ok := positive[op]; ok {
		return "NOT EXISTS (SELECT 1 FROM contact_addresses ca WHERE ca.contact_id = c.id AND " +
			textCondition(expr, p, value, a) + ")"
	}
	return "EXISTS (SELECT 1 FROM contact_addresses ca WHERE ca.contact_id = c.id AND " +
		textCondition(expr, op, value, a) + ")"
}

// attributeCondition tests one key of a jsonb attributes column, e.g. c.custom_attributes.
func attributeCondition(column, attrType, key, op, value string, a *sqlArgs) string {
	k := a.add(key) + "::text"
	text := "COALESCE(" + column + " ->> " + k + ", '')"
	switch op {
	case "is_set":
		return column + " ? " + k
	case "is_not_set":
		return "NOT (" + column + " ? " + k + ")"
	case "is_true":
		return column + " -> " + k + " = 'true'::jsonb"
	case "is_false":
		return column + " -> " + k + " = 'false'::jsonb"
	}
	switch attrType {
	case TypeNumber:
		num := "(CASE WHEN jsonb_typeof(" + column + " -> " + k + ") = 'number' THEN (" + column + " ->> " + k + ")::numeric END)"
		v := a.add(value) + "::numeric"
		switch op {
		case "gt":
			return num + " > " + v
		case "lt":
			return num + " < " + v
		case "not_equals":
			return num + " IS DISTINCT FROM " + v
		default:
			return num + " = " + v
		}
	case TypeDate:
		v := a.add(value) + "::text"
		switch op {
		case "before":
			return "(" + column + " ->> " + k + ") < " + v
		case "on_or_after":
			return "(" + column + " ->> " + k + ") >= " + v
		default:
			return "(" + column + " ->> " + k + ") = " + v
		}
	case TypeList:
		v := a.add(value) + "::text"
		if op == "not_equals" {
			return text + " <> " + v
		}
		return text + " = " + v
	}
	return textCondition(text, op, value, a)
}

// buildCondition renders one validated condition. Contacts are aliased c, their organization
// o. scope renders the restriction to conversations the viewer may read; it is a function so a
// parameter is only bound when the condition uses it.
func buildCondition(c Condition, defs []dbq.CustomAttributeDef, scope func() string, a *sqlArgs) string {
	switch c.Field {
	case FieldName:
		return textCondition("c.name", c.Op, c.Value, a)
	case FieldOrganization:
		return textCondition("COALESCE(o.name, '')", c.Op, c.Value, a)
	case FieldEmail:
		return multiRowCondition("ca.email", c.Op, c.Value, a)
	case FieldDomain:
		return multiRowCondition("split_part(ca.email, '@', 2)", c.Op, c.Value, a)
	case FieldHasOpenConversation:
		exists := "EXISTS (SELECT 1 FROM conversations cv WHERE cv.contact_id = c.id AND cv.deleted_at IS NULL AND cv.status = 'open' AND " + scope() + ")"
		if c.Op == "is_false" {
			return "NOT " + exists
		}
		return exists
	case FieldLastActivity:
		switch c.Op {
		case "before":
			return "c.last_activity_at < " + a.add(c.Value) + "::date"
		case "on_or_after":
			return "c.last_activity_at >= " + a.add(c.Value) + "::date"
		case "within_days":
			return "c.last_activity_at >= now() - (" + a.add(c.Value) + "::integer * interval '1 day')"
		default:
			return "c.last_activity_at < now() - (" + a.add(c.Value) + "::integer * interval '1 day')"
		}
	case FieldAttribute:
		def, _ := findDef(defs, EntityContact, c.Key)
		return attributeCondition("c.custom_attributes", def.Type, c.Key, c.Op, c.Value, a)
	default:
		def, _ := findDef(defs, EntityConversation, c.Key)
		return "EXISTS (SELECT 1 FROM conversations cv WHERE cv.contact_id = c.id AND cv.deleted_at IS NULL AND " + scope() +
			" AND " + attributeCondition("cv.custom_attributes", def.Type, c.Key, c.Op, c.Value, a) + ")"
	}
}

func (f Filter) sql(defs []dbq.CustomAttributeDef, scope func() string, a *sqlArgs) string {
	if len(f.Conditions) == 0 {
		return "TRUE"
	}
	parts := make([]string, len(f.Conditions))
	for i, c := range f.Conditions {
		parts[i] = "(" + buildCondition(c, defs, scope, a) + ")"
	}
	joiner := " AND "
	if f.Match == MatchAny {
		joiner = " OR "
	}
	return strings.Join(parts, joiner)
}
