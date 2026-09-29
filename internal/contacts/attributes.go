package contacts

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"echoo/internal/db/dbq"
)

const (
	EntityContact      = "contact"
	EntityOrganization = "organization"
	EntityConversation = "conversation"

	TypeText    = "text"
	TypeNumber  = "number"
	TypeDate    = "date"
	TypeBoolean = "boolean"
	TypeList    = "list"
	TypeLink    = "link"

	maxTextLen    = 1000
	maxLinkLen    = 2000
	maxOptions    = 50
	maxOptionLen  = 60
	maxAttributes = 100
)

var (
	keyPattern    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	entityNames   = []string{EntityContact, EntityOrganization, EntityConversation}
	attributeType = []string{TypeText, TypeNumber, TypeDate, TypeBoolean, TypeList, TypeLink}
)

func ValidEntity(e string) bool { return slices.Contains(entityNames, e) }

func ValidKey(k string) bool { return keyPattern.MatchString(k) }

func ValidType(t string) bool { return slices.Contains(attributeType, t) }

// CleanOptions trims, de-duplicates and validates the options of a list attribute.
func CleanOptions(typ string, options []string) ([]string, error) {
	if typ != TypeList {
		if len(options) > 0 {
			return nil, errors.New("only list attributes have options")
		}
		return []string{}, nil
	}
	out := make([]string, 0, len(options))
	for _, o := range options {
		o = strings.TrimSpace(o)
		if o == "" || utf8.RuneCountInString(o) > maxOptionLen || hasControl(o) {
			return nil, errors.New("options must be 1 to 60 characters without control characters")
		}
		if !slices.Contains(out, o) {
			out = append(out, o)
		}
	}
	if len(out) == 0 || len(out) > maxOptions {
		return nil, errors.New("a list needs 1 to 50 options")
	}
	return out, nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// Normalize converts one submitted value to the form that is stored for def. A nil value or
// an empty string means "remove" and returns (nil, nil). Text values from a CSV file
// (lenient) may spell numbers, booleans and list options loosely; API values must be typed.
func Normalize(def dbq.CustomAttributeDef, v any, lenient bool) (any, error) {
	if v == nil {
		return nil, nil
	}
	if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
		return nil, nil
	}
	switch def.Type {
	case TypeText:
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("expected text")
		}
		s = strings.TrimSpace(s)
		if utf8.RuneCountInString(s) > maxTextLen || hasControl(s) {
			return nil, errors.New("text is too long or holds control characters")
		}
		return s, nil
	case TypeNumber:
		f, err := toNumber(v, lenient)
		if err != nil {
			return nil, err
		}
		return f, nil
	case TypeDate:
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("expected a date as YYYY-MM-DD")
		}
		s = strings.TrimSpace(s)
		if _, err := time.Parse(time.DateOnly, s); err != nil {
			return nil, errors.New("expected a date as YYYY-MM-DD")
		}
		return s, nil
	case TypeBoolean:
		return toBool(v, lenient)
	case TypeList:
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("expected one of the options")
		}
		s = strings.TrimSpace(s)
		for _, o := range def.Options {
			if o == s || (lenient && strings.EqualFold(o, s)) {
				return o, nil
			}
		}
		return nil, errors.New("not one of the options")
	case TypeLink:
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("expected a link")
		}
		return normalizeLink(s)
	}
	return nil, fmt.Errorf("unknown attribute type %q", def.Type)
}

func toNumber(v any, lenient bool) (float64, error) {
	var f float64
	switch n := v.(type) {
	case float64:
		f = n
	case json.Number:
		parsed, err := n.Float64()
		if err != nil {
			return 0, errors.New("expected a number")
		}
		f = parsed
	case string:
		if !lenient {
			return 0, errors.New("expected a number")
		}
		s := strings.TrimSpace(n)
		// A lone comma is the Dutch decimal separator.
		if strings.Count(s, ",") == 1 && !strings.Contains(s, ".") {
			s = strings.Replace(s, ",", ".", 1)
		}
		parsed, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, errors.New("expected a number")
		}
		f = parsed
	default:
		return 0, errors.New("expected a number")
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) >= 1e15 {
		return 0, errors.New("number is out of range")
	}
	return f, nil
}

func toBool(v any, lenient bool) (bool, error) {
	switch b := v.(type) {
	case bool:
		return b, nil
	case string:
		if lenient {
			switch strings.ToLower(strings.TrimSpace(b)) {
			case "true", "ja", "yes", "1", "waar":
				return true, nil
			case "false", "nee", "no", "0", "onwaar":
				return false, nil
			}
		}
	}
	return false, errors.New("expected true or false")
}

func normalizeLink(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) > maxLinkLen || hasControl(s) {
		return "", errors.New("link is too long")
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", errors.New("expected an http or https link")
	}
	return s, nil
}

// Attributes is a decoded custom_attributes value.
type Attributes map[string]any

func DecodeAttributes(raw []byte) (Attributes, error) {
	out := Attributes{}
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode custom attributes: %w", err)
	}
	return out, nil
}

// ParsePatch decodes a JSON object of attribute changes, keeping numbers exact.
func ParsePatch(raw map[string]json.RawMessage) (map[string]any, error) {
	out := make(map[string]any, len(raw))
	for k, r := range raw {
		dec := json.NewDecoder(strings.NewReader(string(r)))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("attribute %s: invalid value", k)
		}
		switch v.(type) {
		case nil, string, bool, json.Number:
		default:
			return nil, fmt.Errorf("attribute %s: expected a single value", k)
		}
		out[k] = v
	}
	return out, nil
}

// ApplyPatch validates patch against defs and merges it into current. Keys that are not
// defined are rejected, an empty value removes the key. The result is what gets stored.
func ApplyPatch(defs []dbq.CustomAttributeDef, current Attributes, patch map[string]any, lenient bool) (Attributes, map[string]string) {
	byKey := make(map[string]dbq.CustomAttributeDef, len(defs))
	for _, d := range defs {
		byKey[d.Key] = d
	}
	out := make(Attributes, len(current)+len(patch))
	for k, v := range current {
		out[k] = v
	}
	problems := map[string]string{}
	for k, v := range patch {
		def, ok := byKey[k]
		if !ok {
			problems[k] = "unknown_attribute"
			continue
		}
		norm, err := Normalize(def, v, lenient)
		if err != nil {
			problems[k] = "invalid"
			continue
		}
		if norm == nil {
			delete(out, k)
			continue
		}
		out[k] = norm
	}
	if len(out) > maxAttributes && len(problems) == 0 {
		problems["_"] = "too_many"
	}
	if len(problems) > 0 {
		return nil, problems
	}
	return out, nil
}

// Encode is the JSON stored in a custom_attributes column.
func (a Attributes) Encode() ([]byte, error) {
	b, err := json.Marshal(map[string]any(a))
	if err != nil {
		return nil, fmt.Errorf("encode custom attributes: %w", err)
	}
	return b, nil
}
