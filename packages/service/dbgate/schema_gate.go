package dbgate

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/memohai/connect-it/packages/service/dbcontract"
)

const (
	EnvExpectedDatabaseName             = "CONNECT_IT_EXPECTED_DATABASE_NAME"
	EnvExpectedDatabaseSchema           = "CONNECT_IT_EXPECTED_DATABASE_SCHEMA"
	EnvExpectedDatabaseSystemIdentifier = "CONNECT_IT_EXPECTED_DATABASE_SYSTEM_IDENTIFIER"
	EnvExpectedDatabaseApplicationRole  = "CONNECT_IT_EXPECTED_DATABASE_APPLICATION_ROLE"
	EnvExpectedDatabaseOwnerRole        = "CONNECT_IT_EXPECTED_DATABASE_OWNER_ROLE"
)

// ExpectedDatabaseIdentity is the independently approved target identity for
// every physical connection in the production pool.
type ExpectedDatabaseIdentity struct {
	Database         string
	Schema           string
	SystemIdentifier string
	ApplicationRole  string
	OwnerRole        string
}

// startupFailure is a message the gate is willing to put in a production log.
// It is comparable, so errors.Is matches two failures with the same message.
type startupFailure string

func (failure startupFailure) Error() string { return string(failure) }

// ErrDatabaseIdentityInspection is the fail-closed outcome whenever the
// contract cannot be evaluated or reports a code this build does not know.
const ErrDatabaseIdentityInspection = startupFailure(
	"database identity inspection failed",
)

const genericStartupFailure = "database connection or validation failed"

// contractViolations maps every violation code productionContractQuery can
// return to its stable, log-safe failure. A code missing from this table is an
// inspection failure, never a pass.
var contractViolations = map[string]startupFailure{
	"database_name":      "database identity mismatch: database name",
	"schema":             "database identity mismatch: current schema",
	"search_path":        "database identity mismatch: effective search path",
	"system_identifier":  "database identity mismatch: system identifier",
	"recovery":           "database identity mismatch: server is in recovery",
	"read_only":          "database identity mismatch: connection is read-only",
	"application_role":   "database identity mismatch: application role",
	"application_login":  "database identity mismatch: application role cannot login",
	"application_attrs":  "database identity mismatch: application role has forbidden attributes",
	"extended_grants":    "database identity mismatch: application role has forbidden attributes",
	"owner_identity":     "database identity mismatch: database owner role",
	"application_grants": "database identity mismatch: application role privilege profile",
	"table_privileges":   "database identity mismatch: application role table privilege matrix",
	"owner_attrs":        "database identity mismatch: owner role has forbidden attributes",
	"owner_defaults":     "database identity mismatch: owner role default ACL profile",
	"cross_database":     "database identity mismatch: application role has cross-database access",
	"cross_schema":       "database identity mismatch: application role has cross-schema access",
	"routine_privileges": "database identity mismatch: application role routine privilege profile",
	"ownership":          "database identity mismatch: application role owns database objects",
	"schema_rows":        "schema migration row-count mismatch",
	"schema_version":     "schema version is not the exact clean current version",
}

// SafeStartupFailure returns a stable database-gate message safe for
// production logs. Driver and server-controlled details are never exposed.
func SafeStartupFailure(err error) string {
	var safe startupFailure
	if errors.As(err, &safe) {
		return safe.Error()
	}
	return genericStartupFailure
}

// NewProductionDatabaseGate returns the read-only pgxpool.AfterConnect gate.
// The expected values must come from configuration independent of DATABASE_URL.
func NewProductionDatabaseGate(expected ExpectedDatabaseIdentity) (
	func(context.Context, *pgx.Conn) error, error,
) {
	if err := validateExpectedDatabaseIdentity(expected); err != nil {
		return nil, fmt.Errorf("database identity gate configuration: %w", err)
	}
	return func(ctx context.Context, conn *pgx.Conn) error {
		if conn == nil {
			return errors.New("database identity gate connection is nil")
		}
		tables, grantTables, grants := privilegeArguments()
		var violations []string
		if err := conn.QueryRow(
			ctx,
			productionContractQuery,
			expected.Database,
			expected.Schema,
			expected.SystemIdentifier,
			expected.ApplicationRole,
			expected.OwnerRole,
			tables,
			grantTables,
			grants,
			dbcontract.CurrentSchemaVersion,
		).Scan(&violations); err != nil {
			// Driver and server-controlled detail never crosses this boundary.
			return ErrDatabaseIdentityInspection
		}
		return violationFailure(violations)
	}, nil
}

// violationFailure reports the highest-priority violation the contract found.
// The query already orders the codes, so the first one is the one to report.
func violationFailure(violations []string) error {
	if len(violations) == 0 {
		return nil
	}
	failure, known := contractViolations[violations[0]]
	if !known {
		return ErrDatabaseIdentityInspection
	}
	return failure
}

func privilegeArguments() (tables, grantTables, grants []string) {
	matrix := dbcontract.ApplicationTablePrivileges()
	seen := make(map[string]struct{}, len(matrix))
	for _, grant := range matrix {
		grantTables = append(grantTables, grant.Table)
		grants = append(grants, grant.Privilege)
		if _, ok := seen[grant.Table]; ok {
			continue
		}
		seen[grant.Table] = struct{}{}
		tables = append(tables, grant.Table)
	}
	return tables, grantTables, grants
}

// productionContractQuery returns stable violation codes instead of a wide
// positional row. Catalog details remain inside PostgreSQL and never cross the
// startup boundary.
const productionContractQuery = `
	with
	input as (
	  select $1::text as database_name,
	         $2::text as schema_name,
	         $3::text as system_identifier,
	         $4::text as application_role,
	         $5::text as owner_role,
	         $9::bigint as schema_version
	),
	application_role as (
	  select role.*
	  from pg_catalog.pg_roles as role, input
	  where role.rolname = input.application_role
	),
	owner_role as (
	  select role.*
	  from pg_catalog.pg_roles as role, input
	  where role.rolname = input.owner_role
	),
	target_database as (
	  select database.*
	  from pg_catalog.pg_database as database
	  where database.datname = pg_catalog.current_database()
	),
	target_schema as (
	  select namespace.*
	  from pg_catalog.pg_namespace as namespace, input
	  where namespace.nspname = input.schema_name
	),
	control_function as (
	  select routine.*
	  from pg_catalog.pg_proc as routine
	  where routine.oid = 'pg_catalog.pg_control_system()'::pg_catalog.regprocedure
	),
	required_tables(relation_name) as (
	  select pg_catalog.unnest($6::text[])
	),
	required_privileges(relation_name, privilege_name) as (
	  select *
	  from rows from (
	    pg_catalog.unnest($7::text[]),
	    pg_catalog.unnest($8::text[])
	  )
	),
	table_privileges(privilege_name) as (
	  values ('SELECT'), ('INSERT'), ('UPDATE'), ('DELETE'),
	         ('TRUNCATE'), ('REFERENCES'), ('TRIGGER'), ('MAINTAIN')
	),
	target_relations as (
	  select relation.*
	  from pg_catalog.pg_class as relation, target_schema
	  where relation.relnamespace = target_schema.oid
	    and relation.relkind in ('r', 'p')
	),
	marker_relation as (
	  select relation.oid
	  from target_relations as relation
	  where relation.relname = 'schema_migrations'
	    and exists (select 1 from pg_catalog.pg_attribute as attribute
	      where attribute.attrelid = relation.oid and not attribute.attisdropped
	        and attribute.attname = 'version'
	        and attribute.atttypid = 'pg_catalog.int8'::pg_catalog.regtype
	    )
	    and exists (select 1 from pg_catalog.pg_attribute as attribute
	      where attribute.attrelid = relation.oid and not attribute.attisdropped
	        and attribute.attname = 'dirty'
	        and attribute.atttypid = 'pg_catalog.bool'::pg_catalog.regtype
	    )
	),
	schema_marker_document as (
	  select case
	    when pg_catalog.current_database() = input.database_name
	      and pg_catalog.current_schema() = input.schema_name
	      and pg_catalog.current_schemas(false)::text[] =
	          array[input.schema_name]::text[]
	      and session_user::text = input.application_role
	      and current_user::text = input.application_role
	      and pg_catalog.has_table_privilege(
	        (select oid from application_role),
	        (select oid from marker_relation), 'SELECT'
	          )
	    then pg_catalog.query_to_xml(
	      'select count(*) as rows, coalesce(min(version), -1) as version, ' ||
	      'coalesce(bool_or(dirty), true) as dirty from schema_migrations',
	      false, false, ''
	    )
	  end as document
	  from input
	),
	schema_marker as (
	  select coalesce(((pg_catalog.xpath('/table/row/rows/text()',
	           document))[1]::text)::bigint, 0) as rows,
	         coalesce(((pg_catalog.xpath('/table/row/version/text()',
	           document))[1]::text)::bigint, -1) as version,
	         coalesce(((pg_catalog.xpath('/table/row/dirty/text()',
	           document))[1]::text)::boolean, true) as dirty
	  from schema_marker_document
	),
	target_database_acl as (
	  select acl.*
	  from target_database
	  cross join lateral pg_catalog.aclexplode(coalesce(
	    target_database.datacl,
	    pg_catalog.acldefault('d', target_database.datdba)
	  )) as acl
	),
	target_schema_acl as (
	  select acl.*
	  from target_schema
	  cross join lateral pg_catalog.aclexplode(coalesce(
	    target_schema.nspacl,
	    pg_catalog.acldefault('n', target_schema.nspowner)
	  )) as acl
	),
	target_relation_acl as (
	  select relation.relname, acl.*
	  from target_relations as relation
	  cross join lateral pg_catalog.aclexplode(coalesce(
	    relation.relacl,
	    pg_catalog.acldefault('r', relation.relowner)
	  )) as acl
	),
	control_acl as (
	  select control_function.proowner, acl.*
	  from control_function
	  cross join lateral pg_catalog.aclexplode(coalesce(
	    control_function.proacl,
	    pg_catalog.acldefault('f', control_function.proowner)
	  )) as acl
	),
	violations(priority, code) as (
	  select 10, 'database_name'
	  from input
	  where pg_catalog.current_database() <> input.database_name
	  union all select 20, 'schema'
	  from input
	  where pg_catalog.current_schema() is distinct from input.schema_name
	  union all select 30, 'search_path'
	  from input
	  where pg_catalog.current_schemas(false)::text[] is distinct from
	        array[input.schema_name]::text[]
	  union all select 40, 'system_identifier'
	  from input
	  where (pg_catalog.pg_control_system()).system_identifier::text <>
	        input.system_identifier
	  union all select 50, 'recovery'
	  where pg_catalog.pg_is_in_recovery()
	  union all select 60, 'read_only'
	  where pg_catalog.current_setting('transaction_read_only') <> 'off'
	  union all select 70, 'application_role'
	  from input
	  where session_user::text <> input.application_role
	     or current_user::text <> input.application_role
	     or not exists (select 1 from application_role)
	  union all select 80, 'application_login'
	  where (select rolcanlogin from application_role) is distinct from true
	  union all select 90, 'application_attrs'
	  where (select rolinherit from application_role) is distinct from false
	     or coalesce((select rolsuper or rolcreaterole or rolcreatedb
	                         or rolreplication or rolbypassrls
	                  from application_role), true)
	     or exists (select 1 from pg_catalog.pg_auth_members as membership, application_role
	       where membership.member = application_role.oid
	          or membership.roleid = application_role.oid
	     )
	  union all select 100, 'owner_identity'
	  from input
	  where input.application_role = input.owner_role
	     or not exists (select 1 from owner_role)
	     or (select datdba from target_database) is distinct from
	        (select oid from owner_role)
	     or (select nspowner from target_schema) is distinct from
	        (select oid from owner_role)
	  union all select 110, 'application_grants'
	  where coalesce((
	          select not pg_catalog.has_database_privilege(
	                       application_role.oid, target_database.oid, 'CONNECT'
	                     )
	              or pg_catalog.has_database_privilege(
	                   application_role.oid, target_database.oid, 'TEMP'
	                 )
	              or pg_catalog.has_database_privilege(
	                   application_role.oid, target_database.oid, 'CREATE'
	                 )
	          from application_role, target_database
	        ), true)
	     or coalesce((
	          select not pg_catalog.has_schema_privilege(
	                       application_role.oid, target_schema.oid, 'USAGE'
	                     )
	              or pg_catalog.has_schema_privilege(
	                   application_role.oid, target_schema.oid, 'CREATE'
	                 )
	          from application_role, target_schema
	        ), true)
	     or (select pg_catalog.count(*)
	         from target_database_acl as acl, application_role, owner_role
	         where acl.grantee = application_role.oid
	           and acl.grantor = owner_role.oid
	           and acl.privilege_type = 'CONNECT'
	           and not acl.is_grantable) <> 1
	     or exists (select 1 from target_database_acl as acl, application_role, owner_role
	       where acl.grantee not in (application_role.oid, owner_role.oid)
	          or (acl.grantee = application_role.oid and (
	            acl.grantor <> owner_role.oid
	            or acl.privilege_type <> 'CONNECT'
	            or acl.is_grantable
	          ))
	     )
	     or (select pg_catalog.count(*)
	         from target_schema_acl as acl, application_role, owner_role
	         where acl.grantee = application_role.oid
	           and acl.grantor = owner_role.oid
	           and acl.privilege_type = 'USAGE'
	           and not acl.is_grantable) <> 1
	     or exists (select 1 from target_schema_acl as acl, application_role, owner_role
	       where acl.grantee not in (application_role.oid, owner_role.oid)
	          or (acl.grantee = application_role.oid and (
	            acl.grantor <> owner_role.oid
	            or acl.privilege_type <> 'USAGE'
	            or acl.is_grantable
	          ))
	     )
	  union all select 120, 'owner_attrs'
	  where (select rolcanlogin from owner_role) is distinct from false
	     or (select rolinherit from owner_role) is distinct from false
	     or coalesce((select rolsuper or rolcreaterole or rolcreatedb
	                         or rolreplication or rolbypassrls
	                  from owner_role), true)
	     or exists (select 1 from pg_catalog.pg_auth_members as membership, owner_role
	       where membership.member = owner_role.oid
	          or membership.roleid = owner_role.oid
	     )
	  union all select 130, 'owner_defaults'
	  where (select pg_catalog.count(*)
	         from pg_catalog.pg_default_acl as defaults, owner_role
	         where defaults.defaclrole = owner_role.oid) <> 2
	     or exists (select 1 from pg_catalog.pg_default_acl as defaults, owner_role
	       cross join lateral pg_catalog.aclexplode(defaults.defaclacl) as acl
	       where defaults.defaclrole = owner_role.oid
	         and (
	           defaults.defaclnamespace <> 0
	           or defaults.defaclobjtype not in ('f', 'T')
	           or acl.grantee <> owner_role.oid
	           or acl.grantor <> owner_role.oid
	           or acl.is_grantable
	           or acl.privilege_type <> case defaults.defaclobjtype
	             when 'f' then 'EXECUTE' else 'USAGE'
	           end
	         )
	     )
	     or exists (select 1 from pg_catalog.pg_default_acl as defaults, owner_role
	       where defaults.defaclrole = owner_role.oid
	         and (select pg_catalog.count(*)
	              from pg_catalog.aclexplode(defaults.defaclacl)) <> 1
	     )
	  union all select 140, 'cross_database'
	  where exists (select 1 from pg_catalog.pg_database as database, application_role,
	               target_database
	          where database.oid <> target_database.oid
	            and database.datallowconn
	            and pg_catalog.has_database_privilege(
	                  application_role.oid, database.oid, 'CONNECT'
	                )
	        )
	     or exists (select 1 from pg_catalog.pg_database as database, application_role,
	               target_database
	          cross join lateral pg_catalog.aclexplode(coalesce(
	            database.datacl, pg_catalog.acldefault('d', database.datdba)
	          )) as acl
	          where database.oid <> target_database.oid
	            and (database.datdba = application_role.oid
	                 or acl.grantee = application_role.oid)
	        )
	  union all select 150, 'cross_schema'
	  where exists (select 1 from pg_catalog.pg_namespace as namespace, application_role,
	               target_schema
	          where namespace.oid <> target_schema.oid
	            and namespace.nspname <> 'information_schema'
	            and namespace.nspname !~ '^pg_'
	            and (
	              pg_catalog.has_schema_privilege(
	                application_role.oid, namespace.oid, 'USAGE'
	              )
	              or pg_catalog.has_schema_privilege(
	                application_role.oid, namespace.oid, 'CREATE'
	              )
	            )
	        )
	     or exists (select 1 from pg_catalog.pg_namespace as namespace, application_role,
	               target_schema
	          cross join lateral pg_catalog.aclexplode(coalesce(
	            namespace.nspacl,
	            pg_catalog.acldefault('n', namespace.nspowner)
	          )) as acl
	          where namespace.oid <> target_schema.oid
	            and acl.grantee = application_role.oid
	        )
	     or exists (select 1 from pg_catalog.pg_class as relation, application_role,
	               target_schema
	          cross join lateral pg_catalog.aclexplode(coalesce(
	            relation.relacl,
	            pg_catalog.acldefault(
	              (
	                case when relation.relkind = 'S' then 's' else 'r' end
	              )::"char",
	              relation.relowner
	            )
	          )) as acl
	          where relation.relnamespace <> target_schema.oid
	            and acl.grantee = application_role.oid
	        )
	     or exists (select 1 from pg_catalog.pg_type as data_type, application_role,
	               target_schema
	          cross join lateral pg_catalog.aclexplode(data_type.typacl) as acl
	          where data_type.typnamespace <> target_schema.oid
	            and acl.grantee = application_role.oid
	        )
	     or exists (select 1 from pg_catalog.pg_class as relation
	          join pg_catalog.pg_attribute as attribute
	            on attribute.attrelid = relation.oid,
	               application_role, target_schema
	          cross join lateral pg_catalog.aclexplode(
	            attribute.attacl
	          ) as acl
	          where relation.relnamespace <> target_schema.oid
	            and attribute.attnum > 0
	            and not attribute.attisdropped
	            and acl.grantee = application_role.oid
	        )
	     or exists (select 1 from pg_catalog.pg_default_acl as defaults, application_role
	          cross join lateral pg_catalog.aclexplode(defaults.defaclacl) as acl
	          where acl.grantee = application_role.oid
	        )
	  union all select 160, 'ownership'
	  where exists (select 1 from pg_catalog.pg_shdepend as dependency, application_role
	    where dependency.refclassid =
	          'pg_catalog.pg_authid'::pg_catalog.regclass
	      and dependency.refobjid = application_role.oid
	      and dependency.deptype = 'o'
	  )
	  union all select 170, 'table_privileges'
	  where exists (select 1 from required_tables as required
	          where not exists (
	            select 1 from target_relations
	            where target_relations.relname = required.relation_name
	          )
	        )
	     or exists (select 1 from target_relations
	          where not exists (
	            select 1 from required_tables
	            where required_tables.relation_name =
	                  target_relations.relname
	          )
	             or target_relations.relowner is distinct from
	                (select oid from owner_role)
	             or target_relations.relrowsecurity
	             or target_relations.relforcerowsecurity
	        )
	     or exists (select 1 from pg_catalog.pg_class as relation, target_schema
	          where relation.relnamespace = target_schema.oid
	            and relation.relkind in ('S', 'v', 'm', 'f')
	        )
	     or exists (select 1 from target_relations as relation
	          cross join table_privileges as privilege
	          cross join application_role
	          where pg_catalog.has_table_privilege(
	                  application_role.oid, relation.oid,
	                  privilege.privilege_name
	                ) is distinct from exists (
	                  select 1
	                  from required_privileges as required
	                  where required.relation_name = relation.relname
	                    and required.privilege_name =
	                        privilege.privilege_name
	                )
	             or pg_catalog.has_table_privilege(
	                  application_role.oid, relation.oid,
	                  privilege.privilege_name || ' WITH GRANT OPTION'
	                )
	        )
	     or exists (select 1 from target_relation_acl as acl, application_role, owner_role
	          where acl.grantee <> owner_role.oid
	            and not (
	              acl.grantee = application_role.oid
	              and acl.grantor = owner_role.oid
	              and not acl.is_grantable
	              and exists (
	                select 1
	                from required_privileges as required
	                where required.relation_name = acl.relname
	                  and required.privilege_name = acl.privilege_type
	              )
	            )
	        )
	     or exists (select 1 from required_privileges as required
	          join target_relations as relation
	            on relation.relname = required.relation_name
	          cross join application_role
	          cross join owner_role
	          where not exists (
	            select 1
	            from target_relation_acl as acl
	            where acl.relname = relation.relname
	              and acl.grantee = application_role.oid
	              and acl.grantor = owner_role.oid
	              and acl.privilege_type = required.privilege_name
	              and not acl.is_grantable
	          )
	        )
	     or exists (select 1 from target_relations as relation
	          join pg_catalog.pg_attribute as attribute
	            on attribute.attrelid = relation.oid,
	               owner_role
	          cross join lateral pg_catalog.aclexplode(
	            attribute.attacl
	          ) as acl
	          where attribute.attnum > 0
	            and not attribute.attisdropped
	            and acl.grantee <> owner_role.oid
	        )
	     or exists (select 1 from pg_catalog.pg_type as data_type, target_schema, owner_role
	          cross join lateral pg_catalog.aclexplode(data_type.typacl) as acl
	          where data_type.typnamespace = target_schema.oid
	            and (data_type.typowner <> owner_role.oid
	                 or acl.grantee <> owner_role.oid)
	        )
	  union all select 180, 'routine_privileges'
	  where exists (select 1 from pg_catalog.pg_proc as routine, target_schema,
	               application_role
	          where routine.pronamespace = target_schema.oid
	            and pg_catalog.has_function_privilege(
	                  application_role.oid, routine.oid, 'EXECUTE'
	                )
	        )
	     or exists (select 1 from pg_catalog.pg_proc as routine, target_schema, owner_role
	          cross join lateral pg_catalog.aclexplode(coalesce(
	            routine.proacl, pg_catalog.acldefault('f', routine.proowner)
	          )) as acl
	          where routine.pronamespace = target_schema.oid
	            and acl.grantee <> owner_role.oid
	        )
	     or exists (select 1 from pg_catalog.pg_proc as routine
	          join pg_catalog.pg_namespace as namespace
	            on namespace.oid = routine.pronamespace,
	               application_role, target_schema
	          where namespace.oid <> target_schema.oid
	            and namespace.nspname <> 'information_schema'
	            and namespace.nspname !~ '^pg_'
	            and pg_catalog.has_schema_privilege(
	                  application_role.oid, namespace.oid, 'USAGE'
	                )
	            and pg_catalog.has_function_privilege(
	                  application_role.oid, routine.oid, 'EXECUTE'
	                )
	        )
	     or exists (select 1 from pg_catalog.pg_proc as routine
	          join pg_catalog.pg_namespace as namespace
	            on namespace.oid = routine.pronamespace,
	               application_role
	          cross join lateral pg_catalog.aclexplode(coalesce(
	            routine.proacl, pg_catalog.acldefault('f', routine.proowner)
	          )) as acl
	          where acl.grantee = application_role.oid
	            and not (
	              routine.oid = (
	                select oid from control_function
	              )
	              and acl.privilege_type = 'EXECUTE'
	              and acl.grantor = routine.proowner
	              and not acl.is_grantable
	            )
	        )
	     or (select pg_catalog.count(*)
	         from control_acl as acl, application_role
	         where acl.grantee = application_role.oid
	           and acl.privilege_type = 'EXECUTE'
	           and acl.grantor = acl.proowner
	           and not acl.is_grantable) <> 1
	     or (select pg_catalog.count(*)
	         from control_acl as acl, owner_role
	         where acl.grantee = owner_role.oid
	           and acl.privilege_type = 'EXECUTE'
	           and acl.grantor = acl.proowner
	           and not acl.is_grantable) <> 1
	     or exists (select 1 from control_acl as acl, application_role, owner_role
	          where not (
	            acl.grantee in (
	              acl.proowner,
	              application_role.oid,
	              owner_role.oid,
	              coalesce(pg_catalog.to_regrole('pg_monitor')::oid, 0::oid)
	            )
	            and acl.privilege_type = 'EXECUTE'
	            and acl.grantor = acl.proowner
	            and not acl.is_grantable
	          )
	        )
	  union all select 190, 'extended_grants'
	  where exists (select 1 from (
	      select acl.grantee
	      from pg_catalog.pg_language as object
	      cross join lateral pg_catalog.aclexplode(object.lanacl) as acl
	      union all
	      select acl.grantee
	      from pg_catalog.pg_largeobject_metadata as object
	      cross join lateral pg_catalog.aclexplode(object.lomacl) as acl
	      union all
	      select acl.grantee
	      from pg_catalog.pg_tablespace as object
	      cross join lateral pg_catalog.aclexplode(object.spcacl) as acl
	      union all
	      select acl.grantee
	      from pg_catalog.pg_foreign_data_wrapper as object
	      cross join lateral pg_catalog.aclexplode(object.fdwacl) as acl
	      union all
	      select acl.grantee
	      from pg_catalog.pg_foreign_server as object
	      cross join lateral pg_catalog.aclexplode(object.srvacl) as acl
	      union all
	      select acl.grantee
	      from pg_catalog.pg_parameter_acl as object
	      cross join lateral pg_catalog.aclexplode(object.paracl) as acl
	    ) as grant_row, application_role
	    where grant_row.grantee = application_role.oid
	  )
	  union all select 200, 'schema_rows'
	  from schema_marker
	  where schema_marker.rows <> 1
	  union all select 210, 'schema_version'
	  from schema_marker, input
	  where schema_marker.rows = 1
	    and (
	      schema_marker.version <> input.schema_version
	      or schema_marker.dirty
	    )
	)
	select coalesce(
	  pg_catalog.array_agg(code order by priority, code),
	  '{}'::text[]
	)
	from violations
`

func validateExpectedDatabaseIdentity(expected ExpectedDatabaseIdentity) error {
	switch {
	case strings.TrimSpace(expected.Database) == "":
		return errors.New("expected database name is required")
	case strings.TrimSpace(expected.Schema) == "":
		return errors.New("expected database schema is required")
	case strings.TrimSpace(expected.SystemIdentifier) == "":
		return errors.New("expected database system identifier is required")
	case strings.TrimSpace(expected.ApplicationRole) == "":
		return errors.New("expected database application role is required")
	case strings.TrimSpace(expected.OwnerRole) == "":
		return errors.New("expected database owner role is required")
	case expected.ApplicationRole == expected.OwnerRole:
		return errors.New("expected database application and owner roles must be distinct")
	}
	systemIdentifier, err := strconv.ParseUint(expected.SystemIdentifier, 10, 64)
	if err != nil ||
		systemIdentifier == 0 ||
		strconv.FormatUint(systemIdentifier, 10) != expected.SystemIdentifier {
		return errors.New("expected database system identifier must be a canonical non-zero uint64")
	}
	return nil
}
