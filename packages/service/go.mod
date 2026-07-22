module github.com/memohai/connect-it/packages/service

go 1.25.7

require (
	github.com/golang-migrate/migrate/v4 v4.19.1
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.10.0
	github.com/memohai/connect-it/packages/core v0.0.0-00010101000000-000000000000
)

require (
	github.com/jackc/pgerrcode v0.0.0-20220416144525-469b46aa5efa // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.18.0 // indirect
	golang.org/x/text v0.31.0 // indirect
)

replace github.com/memohai/connect-it/packages/core => ../core

replace github.com/memohai/connect-it/packages/connectors => ../connectors
