// Package smoketest is the shared harness behind every connector's opt-in
// real-account smoke test (`<connector>/real_smoke_test.go`).
//
// Those harnesses never run in CI: each one is gated on its own environment
// variables and skips when they are absent. That is exactly why the scaffolding
// lives here — nine private copies of an env gate, a Tool runner and a cleanup
// retry loop drifted apart without anything noticing, and a drifted cleanup loop
// leaks real objects into a real account.
//
// Three rules this package exists to write down once:
//
//   - A partially configured harness fails closed. Setting one variable of a
//     set arms the harness; the rest are then mandatory.
//   - A harness that mutates a real account is gated on an exact literal, so a
//     stray "1" or "TRUE" can never enable a write.
//   - Nothing here ever formats a Provider body, a Provider error string or a
//     credential into test output. Every message is fixed text plus a public
//     failure code.
//
// The package imports testing, so it is test-only in spirit; the guard in
// import_test.go makes that structural by rejecting any production import.
package smoketest

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Lookup resolves one environment variable, reporting whether it is set at all.
// os.LookupEnv is the production implementation; MapLookup is the test one.
type Lookup func(string) (string, bool)

// MapLookup adapts a fixture map to Lookup.
func MapLookup(values map[string]string) Lookup {
	return func(name string) (string, bool) {
		value, exists := values[name]
		return value, exists
	}
}

// Values reads every name through lookup. The harness counts as configured as
// soon as any one name is set, and a configured harness must then have all of
// them: half a real-account configuration is an operator mistake, not a reason
// to silently skip.
func Values(
	provider string,
	lookup Lookup,
	names ...string,
) (map[string]string, bool, error) {
	values := make(map[string]string, len(names))
	configured := false
	for _, name := range names {
		value, exists := lookup(name)
		values[name] = value
		configured = configured || exists
	}
	if !configured {
		return nil, false, nil
	}
	for _, name := range names {
		if values[name] == "" {
			return nil, true, fmt.Errorf(
				"%s real smoke is partially configured: %s is required",
				provider,
				name,
			)
		}
	}
	return values, true, nil
}

// Exactly requires name to hold one of want verbatim. Every write gate and
// every network opt-in goes through it, so no truthy-looking value can arm a
// mutating or plain-HTTP run by accident.
func Exactly(values map[string]string, name string, want ...string) error {
	value := values[name]
	for _, candidate := range want {
		if value == candidate {
			return nil
		}
	}
	return fmt.Errorf(
		"%s must be exactly %s",
		name,
		strings.Join(want, " or "),
	)
}

// Config resolves a harness configuration from the real environment: skip when
// nothing is configured, fail when the configuration is incomplete or invalid.
func Config[T any](
	t *testing.T,
	provider string,
	parse func(Lookup) (T, bool, error),
) T {
	t.Helper()
	config, configured, err := parse(os.LookupEnv)
	if !configured {
		t.Skip(provider + " real smoke environment is not configured")
	}
	if err != nil {
		t.Fatal(err)
	}
	return config
}

// Marker returns the unique, greppable identity a smoke-created resource is
// named after. Cleanup resolves the resource through this marker when the
// mutation's own response is lost, so it carries both a UTC stamp and 128 bits
// of entropy.
func Marker(t *testing.T) string {
	t.Helper()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal("generate smoke resource marker")
	}
	return time.Now().UTC().Format("20060102T150405.000000000Z") +
		"-" + hex.EncodeToString(nonce[:])
}
