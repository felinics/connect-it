package connector_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
)

func TestToolFailurePublicContract(t *testing.T) {
	failure := connector.Failure(
		connector.FailureRateLimited,
		"provider rate limit exceeded",
		429,
		172800,
	)
	if !failure.Valid() {
		t.Fatalf("valid constructor produced invalid failure: %#v", failure)
	}
	if normalized := connector.NormalizeToolFailure(failure); normalized != *failure {
		t.Fatalf("normalized failure = %#v, want %#v", normalized, *failure)
	}

	invalidCredential := connector.CredentialInvalidFailure(
		"provider credential is invalid",
		401,
	)
	if !invalidCredential.IndicatesCredentialInvalid() {
		t.Fatalf("credential-invalid signal = %#v", invalidCredential)
	}
	if normalized := connector.NormalizeToolFailure(invalidCredential); !normalized.IndicatesCredentialInvalid() {
		t.Fatalf("normalization dropped internal signal: %#v", normalized)
	}
}

func TestToolFailureRejectsUnsafePublicShape(t *testing.T) {
	cases := []connector.ToolFailure{
		{Code: "made_up", Message: "message"},
		{Code: connector.FailureProviderError, Message: ""},
		{
			Code:    connector.FailureProviderError,
			Message: strings.Repeat("x", connector.MaxSafeMessageBytes+1),
		},
		{Code: connector.FailureProviderError, Message: " secret\nvalue"},
		{
			Code:           connector.FailureProviderError,
			Message:        "message",
			UpstreamStatus: 99,
		},
		{
			Code:              connector.FailureProviderError,
			Message:           "message",
			RetryAfterSeconds: -1,
		},
	}
	for _, input := range cases {
		input := input
		t.Run(string(input.Code)+input.Message, func(t *testing.T) {
			if input.Valid() {
				t.Fatalf("unsafe failure accepted: %#v", input)
			}
			// 校验型构造器（api/errors.go 用于 Provider 提供的值）返回错误。
			if _, err := connector.NewToolFailure(
				input.Code,
				input.Message,
				input.UpstreamStatus,
				input.RetryAfterSeconds,
			); !errors.Is(err, connector.ErrInvalidToolFailure) {
				t.Fatalf("constructor error = %v", err)
			}
			// 夹紧型构造器与 NormalizeToolFailure 一律 fail-closed，
			// 且绝不回显非法 code/message/status。
			assertClamped(t, connector.Failure(
				input.Code,
				input.Message,
				input.UpstreamStatus,
				input.RetryAfterSeconds,
			))
			got := connector.NormalizeToolFailure(&input)
			assertClamped(t, &got)
		})
	}

	malformedSignal := connector.ToolFailure{
		Code:              connector.FailurePermissionDenied,
		Message:           "provider permission denied",
		UpstreamStatus:    403,
		CredentialInvalid: true,
	}
	if malformedSignal.Valid() ||
		malformedSignal.IndicatesCredentialInvalid() {
		t.Fatalf("invalid signal accepted: %#v", malformedSignal)
	}
	normalized := connector.NormalizeToolFailure(&malformedSignal)
	assertClamped(t, &normalized)
}

// CredentialInvalidFailure 只能配 authorization_failed，非法输入同样被夹紧。
func TestCredentialInvalidFailureClampsUnsafeInput(t *testing.T) {
	assertClamped(t, connector.CredentialInvalidFailure("", 401))
	assertClamped(t, connector.CredentialInvalidFailure(
		strings.Repeat("x", connector.MaxSafeMessageBytes+1),
		401,
	))
	assertClamped(t, connector.CredentialInvalidFailure(
		"provider credential is invalid",
		99,
	))
}

func TestOrdinaryAuthorizationFailureAndForbiddenAreStateNeutral(t *testing.T) {
	ordinary := connector.Failure(
		connector.FailureAuthorizationFailed,
		"provider authorization failed",
		401,
		0,
	)
	if ordinary.IndicatesCredentialInvalid() {
		t.Fatal("ordinary authorization_failed became credential-invalid signal")
	}
	forbidden := connector.Failure(
		connector.FailurePermissionDenied,
		"provider permission denied",
		403,
		0,
	)
	if forbidden.IndicatesCredentialInvalid() {
		t.Fatal("403 became credential-invalid signal")
	}
}

func TestFailureCodeTemporary(t *testing.T) {
	temporary := map[connector.FailureCode]bool{
		connector.FailureRateLimited:         true,
		connector.FailureUpstreamUnavailable: true,
		connector.FailureTimeout:             true,
		connector.FailureAuthorizationFailed: false,
		connector.FailurePermissionDenied:    false,
		connector.FailureInvalidResponse:     false,
		connector.FailureCanceled:            false,
		connector.FailureInternalError:       false,
		"made_up":                            false,
	}
	for code, want := range temporary {
		if got := code.Temporary(); got != want {
			t.Errorf("%q.Temporary() = %v, want %v", code, got, want)
		}
	}
}

func TestFailureCodeForStatus(t *testing.T) {
	tests := map[int]connector.FailureCode{
		http.StatusUnauthorized:        connector.FailureAuthorizationFailed,
		http.StatusForbidden:           connector.FailurePermissionDenied,
		http.StatusNotFound:            connector.FailureNotFound,
		http.StatusConflict:            connector.FailureConflict,
		http.StatusTooManyRequests:     connector.FailureRateLimited,
		http.StatusBadRequest:          connector.FailureProviderError,
		http.StatusInternalServerError: connector.FailureUpstreamUnavailable,
		599:                            connector.FailureUpstreamUnavailable,
		http.StatusOK:                  "",
		0:                              "",
	}
	for status, want := range tests {
		if got := connector.FailureCodeForStatus(status); got != want {
			t.Errorf("FailureCodeForStatus(%d) = %q, want %q", status, got, want)
		}
	}
}

func assertClamped(t *testing.T, got *connector.ToolFailure) {
	t.Helper()
	if got == nil ||
		got.Code != connector.FailureInternalError ||
		got.Message != "internal error" ||
		got.UpstreamStatus != 0 ||
		got.RetryAfterSeconds != 0 ||
		got.CredentialInvalid {
		t.Fatalf("未夹紧的失败: %#v", got)
	}
}
