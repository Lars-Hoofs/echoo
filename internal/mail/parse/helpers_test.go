package parse

import (
	"reflect"
	"strings"
	"testing"
)

func eq[T any](t *testing.T, what string, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %#v, want %#v", what, got, want)
	}
}

func contains(t *testing.T, what, got string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(got, s) {
			t.Errorf("%s does not contain %q:\n%s", what, s, got)
		}
	}
}

func notContains(t *testing.T, what, got string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if strings.Contains(got, s) {
			t.Errorf("%s contains %q:\n%s", what, s, got)
		}
	}
}
