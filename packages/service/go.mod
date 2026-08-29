module github.com/felinics/connect-it/packages/service

go 1.25.7

require (
	github.com/felinics/connect-it/packages/core v0.0.0-00010101000000-000000000000
	github.com/golang-migrate/migrate/v4 v4.19.1
	github.com/google/jsonschema-go v0.4.3
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.10.0
	github.com/modelcontextprotocol/go-sdk v1.7.0
	golang.org/x/crypto v0.45.0
	golang.org/x/sync v0.20.0
)

require (
	github.com/jackc/pgerrcode v0.0.0-20220416144525-469b46aa5efa // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/text v0.31.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)

replace github.com/felinics/connect-it/packages/core => ../core

replace github.com/felinics/connect-it/packages/connectors => ../connectors
