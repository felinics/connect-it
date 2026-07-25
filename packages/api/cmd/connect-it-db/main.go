// connect-it-db initializes a fresh database schema. It intentionally exposes
// only init: there are no upgrade, down, force, or cutover commands.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/memohai/connect-it/packages/service/dbmigrate"
)

type initializeFunc func(context.Context, string) error

func main() {
	os.Exit(run(
		os.Args[1:],
		os.Getenv,
		dbmigrate.Initialize,
		os.Stdout,
		os.Stderr,
	))
}

func run(
	args []string,
	getenv func(string) string,
	initialize initializeFunc,
	stdout io.Writer,
	stderr io.Writer,
) int {
	if len(args) != 1 || args[0] != "init" {
		fmt.Fprintln(stderr, "usage: connect-it-db init")
		return 2
	}
	databaseURL := strings.TrimSpace(getenv("DATABASE_URL"))
	if databaseURL == "" {
		fmt.Fprintln(stderr, "connect-it-db: DATABASE_URL is required")
		return 2
	}
	if err := initialize(context.Background(), databaseURL); err != nil {
		fmt.Fprintln(stderr, "connect-it-db: database initialization failed")
		return 1
	}
	fmt.Fprintln(stdout, "database schema is current")
	return 0
}
