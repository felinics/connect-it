package dbcontract

import "testing"

func TestApplicationTablePrivilegesAreImmutableAndWellFormed(t *testing.T) {
	t.Parallel()

	privileges := ApplicationTablePrivileges()
	if len(privileges) == 0 {
		t.Fatal("application table privilege matrix is empty")
	}
	seen := make(map[TablePrivilege]bool, len(privileges))
	for _, privilege := range privileges {
		if privilege.Table == "" || privilege.Privilege == "" {
			t.Fatalf("invalid privilege %#v", privilege)
		}
		if seen[privilege] {
			t.Fatalf("duplicate privilege %#v", privilege)
		}
		seen[privilege] = true
	}

	first := privileges[0]
	privileges[0] = TablePrivilege{Table: "mutated", Privilege: "DELETE"}
	if fresh := ApplicationTablePrivileges()[0]; fresh != first {
		t.Fatalf("caller mutated canonical matrix: %#v", fresh)
	}
}
