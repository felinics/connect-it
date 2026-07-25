package testkit

import (
	"net/http"
	"reflect"
	"slices"
	"testing"
)

func TestHeaderValuesFoldPreservesPresenceAndAllValues(t *testing.T) {
	t.Parallel()

	header := http.Header{
		"x-api-key": {"first"},
		"X-Api-Key": {"second", "third"},
		"Other":     {"value"},
	}
	values, present := headerValuesFold(header, "X-API-KEY")
	if !present {
		t.Fatal("case-insensitive header was reported absent")
	}
	slices.Sort(values)
	if want := []string{"first", "second", "third"}; !reflect.DeepEqual(
		values,
		want,
	) {
		t.Fatalf("values = %#v, want %#v", values, want)
	}

	values, present = headerValuesFold(
		http.Header{"X-Empty": {""}},
		"x-empty",
	)
	if !present || !reflect.DeepEqual(values, []string{""}) {
		t.Fatalf("empty present header = (%#v, %v)", values, present)
	}

	if values, present = headerValuesFold(header, "Missing"); present ||
		values != nil {
		t.Fatalf("missing header = (%#v, %v)", values, present)
	}
}
