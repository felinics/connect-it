// Package migrations embeds the single fresh-database schema baseline.
package migrations

import _ "embed"

//go:embed 0001_baseline.up.sql
var baselineSQL string

// BaselineSQL returns the immutable schema used to initialize an empty
// database schema.
func BaselineSQL() string {
	return baselineSQL
}
