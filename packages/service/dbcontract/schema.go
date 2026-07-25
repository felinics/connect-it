// Package dbcontract contains the dependency-free production schema contract.
package dbcontract

// CurrentSchemaVersion is the only schema version accepted by the production
// server and the version produced by fresh-database initialization.
const CurrentSchemaVersion = 1

// TablePrivilege is one required application-role grant on a schema table.
type TablePrivilege struct {
	Table     string
	Privilege string
}

var applicationTablePrivileges = [...]TablePrivilege{
	{"admin_account", "SELECT"},
	{"admin_account", "INSERT"},
	{"admin_account", "UPDATE"},
	{"api_tokens", "SELECT"},
	{"api_tokens", "INSERT"},
	{"api_tokens", "UPDATE"},
	{"connector_configs", "SELECT"},
	{"connector_configs", "INSERT"},
	{"connector_configs", "UPDATE"},
	{"connector_configs", "DELETE"},
	{"connections", "SELECT"},
	{"connections", "INSERT"},
	{"connections", "UPDATE"},
	{"connections", "DELETE"},
	{"oauth_authorizations", "SELECT"},
	{"oauth_authorizations", "INSERT"},
	{"oauth_authorizations", "UPDATE"},
	{"oauth_authorizations", "DELETE"},
	{"mcp_sessions", "SELECT"},
	{"mcp_sessions", "INSERT"},
	{"mcp_session_connections", "SELECT"},
	{"mcp_session_connections", "INSERT"},
	{"connector_health", "SELECT"},
	{"connector_health", "INSERT"},
	{"connector_health", "UPDATE"},
	{"tool_runs", "INSERT"},
	{"connector_policy_identities", "SELECT"},
	{"connector_policy_identities", "INSERT"},
	{"connector_policy_identities", "UPDATE"},
	{"schema_migrations", "SELECT"},
}

// ApplicationTablePrivileges returns a copy of the production application
// role's complete table privilege matrix.
func ApplicationTablePrivileges() []TablePrivilege {
	privileges := make([]TablePrivilege, len(applicationTablePrivileges))
	copy(privileges, applicationTablePrivileges[:])
	return privileges
}
