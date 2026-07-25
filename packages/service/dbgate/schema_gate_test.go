package dbgate

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/service/dbcontract"
)

var validIdentity = ExpectedDatabaseIdentity{
	Database:         "connect_it",
	Schema:           "public",
	SystemIdentifier: "7654321",
	ApplicationRole:  "connect_it_app",
	OwnerRole:        "connect_it_owner",
}

// A gate that cannot state exactly which database it expects must not be
// built at all, so every rejected configuration is pinned here.
func TestValidateExpectedDatabaseIdentity(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		change func(*ExpectedDatabaseIdentity)
		want   string
	}{
		"valid": {},
		"database required": {
			change: func(value *ExpectedDatabaseIdentity) { value.Database = " " },
			want:   "database name is required",
		},
		"schema required": {
			change: func(value *ExpectedDatabaseIdentity) { value.Schema = "" },
			want:   "database schema is required",
		},
		"system identifier required": {
			change: func(value *ExpectedDatabaseIdentity) {
				value.SystemIdentifier = ""
			},
			want: "system identifier is required",
		},
		"application role required": {
			change: func(value *ExpectedDatabaseIdentity) {
				value.ApplicationRole = ""
			},
			want: "application role is required",
		},
		"owner role required": {
			change: func(value *ExpectedDatabaseIdentity) { value.OwnerRole = "" },
			want:   "owner role is required",
		},
		"roles distinct": {
			change: func(value *ExpectedDatabaseIdentity) {
				value.OwnerRole = value.ApplicationRole
			},
			want: "must be distinct",
		},
		"zero identifier": {
			change: func(value *ExpectedDatabaseIdentity) {
				value.SystemIdentifier = "0"
			},
			want: "canonical non-zero uint64",
		},
		"leading zero identifier": {
			change: func(value *ExpectedDatabaseIdentity) {
				value.SystemIdentifier = "07654321"
			},
			want: "canonical non-zero uint64",
		},
		"negative identifier": {
			change: func(value *ExpectedDatabaseIdentity) {
				value.SystemIdentifier = "-1"
			},
			want: "canonical non-zero uint64",
		},
		"overflow identifier": {
			change: func(value *ExpectedDatabaseIdentity) {
				value.SystemIdentifier = "18446744073709551616"
			},
			want: "canonical non-zero uint64",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			identity := validIdentity
			if test.change != nil {
				test.change(&identity)
			}
			gate, err := NewProductionDatabaseGate(identity)
			if test.want == "" {
				if err != nil || gate == nil {
					t.Fatalf("NewProductionDatabaseGate: gate=%v err=%v", gate != nil, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// The contract query returns codes, not rows; this is the whole dispatch.
func TestViolationFailure(t *testing.T) {
	t.Parallel()

	if err := violationFailure(nil); err != nil {
		t.Fatalf("no violations = %v, want nil", err)
	}
	for code, want := range contractViolations {
		if err := violationFailure([]string{code}); !errors.Is(err, want) {
			t.Errorf("violationFailure(%q) = %v, want %v", code, err, want)
		}
	}
	// The query orders codes by priority, so the first one is reported.
	if err := violationFailure([]string{"database_name", "schema"}); !errors.Is(
		err,
		contractViolations["database_name"],
	) {
		t.Fatalf("first violation = %v", err)
	}
	// A newer server-side code must fail closed, never pass.
	if err := violationFailure([]string{"future_code"}); !errors.Is(
		err,
		ErrDatabaseIdentityInspection,
	) {
		t.Fatalf("unknown violation = %v", err)
	}
}

func TestProductionDatabaseGateRejectsNilConnection(t *testing.T) {
	t.Parallel()

	gate, err := NewProductionDatabaseGate(validIdentity)
	if err != nil {
		t.Fatalf("NewProductionDatabaseGate: %v", err)
	}
	err = gate(t.Context(), nil)
	if err == nil || !strings.Contains(err.Error(), "connection is nil") {
		t.Fatalf("error = %v", err)
	}
	if got := SafeStartupFailure(err); got != genericStartupFailure {
		t.Fatalf("SafeStartupFailure = %q", got)
	}
}

// Only the gate's own messages may reach a production log.
func TestSafeStartupFailure(t *testing.T) {
	t.Parallel()

	for _, safe := range append(
		[]startupFailure{ErrDatabaseIdentityInspection},
		contractViolationValues()...,
	) {
		if got := SafeStartupFailure(fmt.Errorf("context: %w", safe)); got != safe.Error() {
			t.Errorf("SafeStartupFailure(%v) = %q", safe, got)
		}
	}
	if got := SafeStartupFailure(errors.New("password=secret")); got != genericStartupFailure {
		t.Fatalf("SafeStartupFailure(unknown) = %q", got)
	}
}

// The contract compares $7[i] against $8[i]; a shift between the two arrays
// would approve the wrong privilege on the wrong table.
func TestPrivilegeArgumentsStayAlignedWithCanonicalMatrix(t *testing.T) {
	t.Parallel()

	matrix := dbcontract.ApplicationTablePrivileges()
	tables, grantTables, grants := privilegeArguments()
	if len(grantTables) != len(matrix) || len(grants) != len(matrix) {
		t.Fatalf(
			"matrix=%d tables=%d grants=%d",
			len(matrix),
			len(grantTables),
			len(grants),
		)
	}
	seen := map[string]bool{}
	var wantTables []string
	for index, grant := range matrix {
		if grantTables[index] != grant.Table || grants[index] != grant.Privilege {
			t.Fatalf("argument %d does not match canonical matrix", index)
		}
		if !seen[grant.Table] {
			seen[grant.Table] = true
			wantTables = append(wantTables, grant.Table)
		}
	}
	if !reflect.DeepEqual(tables, wantTables) {
		t.Fatalf("tables = %#v, want %#v", tables, wantTables)
	}
}

func contractViolationValues() []startupFailure {
	failures := make([]startupFailure, 0, len(contractViolations))
	for _, failure := range contractViolations {
		failures = append(failures, failure)
	}
	return failures
}
