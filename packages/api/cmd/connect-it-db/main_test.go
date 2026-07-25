package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRunInitializesOnlyWithExplicitInit(t *testing.T) {
	t.Parallel()

	var (
		gotURL string
		output bytes.Buffer
	)
	code := run(
		[]string{"init"},
		func(key string) string {
			if key != "DATABASE_URL" {
				t.Fatalf("unexpected environment lookup %q", key)
			}
			return "postgres://database.example/connect_it"
		},
		func(_ context.Context, databaseURL string) error {
			gotURL = databaseURL
			return nil
		},
		&output,
		&output,
	)
	if code != 0 {
		t.Fatalf("exit code = %d, output %q", code, output.String())
	}
	if gotURL != "postgres://database.example/connect_it" {
		t.Fatalf("initializer URL = %q", gotURL)
	}
	if output.String() != "database schema is current\n" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRunRejectsEveryOtherOperation(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		nil,
		{"up"},
		{"down"},
		{"force"},
		{"cutover"},
		{"init", "extra"},
	} {
		args := args
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			t.Parallel()

			var output bytes.Buffer
			called := false
			code := run(
				args,
				func(string) string { return "unused" },
				func(context.Context, string) error {
					called = true
					return nil
				},
				&output,
				&output,
			)
			if code != 2 || called {
				t.Fatalf(
					"args %q: code=%d called=%t output=%q",
					args,
					code,
					called,
					output.String(),
				)
			}
			if output.String() != "usage: connect-it-db init\n" {
				t.Fatalf("args %q: output=%q", args, output.String())
			}
		})
	}
}

func TestRunRequiresDatabaseURL(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	called := false
	code := run(
		[]string{"init"},
		func(string) string { return " \t " },
		func(context.Context, string) error {
			called = true
			return nil
		},
		&output,
		&output,
	)
	if code != 2 || called {
		t.Fatalf(
			"code=%d called=%t output=%q",
			code,
			called,
			output.String(),
		)
	}
}

func TestRunDoesNotPrintInitializerError(t *testing.T) {
	t.Parallel()

	const secret = "DO_NOT_PRINT_DATABASE_SECRET"
	var output bytes.Buffer
	code := run(
		[]string{"init"},
		func(string) string { return "postgres://secret" },
		func(context.Context, string) error {
			return errors.New(secret)
		},
		&output,
		&output,
	)
	if code != 1 {
		t.Fatalf("code=%d output=%q", code, output.String())
	}
	if strings.Contains(output.String(), secret) {
		t.Fatalf("initializer error leaked: %q", output.String())
	}
}
