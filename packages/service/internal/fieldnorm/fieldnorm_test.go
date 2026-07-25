package fieldnorm

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/service/credential"
)

func TestNormalizeAppliesDefaultsAndReturnsDefensiveMap(t *testing.T) {
	defaultRegion := "us"
	definitions := []connector.ConfigField{
		{
			Key:        "token",
			Required:   true,
			Validation: connector.FieldValidation{Pattern: `^tok_`},
		},
		{
			Key:          "region",
			DefaultValue: &defaultRegion,
			Validation: connector.FieldValidation{
				Options: []string{"us", "eu"},
			},
		},
	}
	input := map[string]string{"token": "tok_secret"}
	normalized, err := Normalize(definitions, input)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"token": "tok_secret", "region": "us"}
	if !reflect.DeepEqual(normalized, want) {
		t.Fatalf("normalized = %#v, want %#v", normalized, want)
	}
	normalized["token"] = "changed"
	if input["token"] != "tok_secret" {
		t.Fatal("Normalize aliased the caller's input map")
	}
}

func TestNormalizeRejectsUnknownMissingPatternOptionsAndExplicitEmpty(t *testing.T) {
	defaultRegion := "us"
	definitions := []connector.ConfigField{
		{Key: "token", Required: true, Validation: connector.FieldValidation{Pattern: `^tok_`}},
		{
			Key:          "region",
			Required:     true,
			DefaultValue: &defaultRegion,
			Validation: connector.FieldValidation{
				Options: []string{"us", "eu"},
			},
		},
	}
	for _, test := range []struct {
		name  string
		input map[string]string
		field string
	}{
		{name: "unknown", input: map[string]string{"unknown": "x"}, field: "unknown"},
		{name: "missing required", input: map[string]string{}, field: "token"},
		{name: "pattern", input: map[string]string{"token": "bad"}, field: "token"},
		{
			name:  "options",
			input: map[string]string{"token": "tok_ok", "region": "ap"},
			field: "region",
		},
		{
			name:  "explicit empty does not select default",
			input: map[string]string{"token": "tok_ok", "region": ""},
			field: "region",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Normalize(definitions, test.input)
			var fieldErr *Error
			if !errors.As(err, &fieldErr) || fieldErr.Field != test.field {
				t.Fatalf("error = %#v, want field %q", err, test.field)
			}
		})
	}
}

func TestNormalizeRejectsInvalidDefinitionsWithoutEchoingValue(t *testing.T) {
	for _, definitions := range [][]connector.ConfigField{
		{{Key: ""}},
		{{Key: "same"}, {Key: "same"}},
		{{Key: "value", Validation: connector.FieldValidation{Pattern: `[`}}},
	} {
		_, err := Normalize(definitions, map[string]string{"value": "secret-value"})
		if err == nil {
			t.Fatal("invalid definition was accepted")
		}
		if got := err.Error(); got == "secret-value" {
			t.Fatal("normalization error exposed the rejected value")
		}
	}
}

func TestNormalizeCanonicalizesInputURLForValidationAndPersistence(t *testing.T) {
	defaultURL := "HTTPS://GitLab.COM/api/v4/"
	definitions := []connector.ConfigField{{
		Key:          "base_url",
		InputType:    connector.InputURL,
		Required:     true,
		DefaultValue: &defaultURL,
	}}
	for _, test := range []struct {
		name  string
		input map[string]string
		want  string
	}{
		{
			name:  "default",
			input: map[string]string{},
			want:  "https://gitlab.com:443/api/v4/",
		},
		{
			name: "explicit",
			input: map[string]string{
				"base_url": "http://GitLab.Internal:8080/root",
			},
			want: "http://gitlab.internal:8080/root",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := Normalize(definitions, test.input)
			if err != nil {
				t.Fatal(err)
			}
			if normalized["base_url"] != test.want {
				t.Fatalf("base_url = %q, want %q", normalized["base_url"], test.want)
			}
			payload, err := (credential.Fields{Fields: normalized}).Marshal()
			if err != nil {
				t.Fatal(err)
			}
			stored, err := credential.UnmarshalFields(payload)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Fields["base_url"] != test.want {
				t.Fatalf("stored base_url = %q", stored.Fields["base_url"])
			}
		})
	}
}

func TestNormalizeRejectsUnsafeInputURLWithoutEchoingIt(t *testing.T) {
	definitions := []connector.ConfigField{{
		Key:       "base_url",
		InputType: connector.InputURL,
		Required:  true,
	}}
	const secret = "secret-in-query"
	for _, value := range []string{
		"https://user:password@example.com",
		"https://example.com/api?token=" + secret,
		"https://example.com/api#fragment",
		"https://example.com/a/../api",
		"ftp://example.com/api",
		"https://例.example/api",
	} {
		_, err := Normalize(definitions, map[string]string{"base_url": value})
		if err == nil {
			t.Fatalf("unsafe URL accepted")
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatal("normalization error exposed URL query data")
		}
	}
}
