package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/service/oauthsvc"
)

func TestCredentialValidationErrorHTTPMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		code       connector.FailureCode
		status     int
		temporary  bool
		retryAfter int
	}{
		{"invalid input", connector.FailureInvalidInput, http.StatusUnprocessableEntity, false, 0},
		{"authorization", connector.FailureAuthorizationFailed, http.StatusUnauthorized, false, 0},
		{"permission", connector.FailurePermissionDenied, http.StatusForbidden, false, 0},
		{"rate limit", connector.FailureRateLimited, http.StatusTooManyRequests, true, 17},
		{"provider unavailable", connector.FailureUpstreamUnavailable, http.StatusServiceUnavailable, true, 0},
		{"timeout", connector.FailureTimeout, http.StatusGatewayTimeout, true, 0},
		{"canceled", connector.FailureCanceled, http.StatusRequestTimeout, false, 0},
		{"invalid response", connector.FailureInvalidResponse, http.StatusBadGateway, false, 0},
		{"response too large", connector.FailureResponseTooLarge, http.StatusBadGateway, false, 0},
		{"provider error", connector.FailureProviderError, http.StatusBadGateway, false, 0},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			echoServer := echo.New()
			recorder := httptest.NewRecorder()
			context := echoServer.NewContext(
				httptest.NewRequest(http.MethodPost, "/v1/connections/api-key", nil),
				recorder,
			)
			validationErr := &connector.CredentialValidationError{
				Code:              test.code,
				SafeMessage:       "credential validation failed safely",
				Temporary:         test.temporary,
				RetryAfterSeconds: test.retryAfter,
			}

			if err := mapServiceError(context, validationErr); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != test.status {
				t.Fatalf(
					"status = %d, want %d; body=%s",
					recorder.Code,
					test.status,
					recorder.Body,
				)
			}
			var response ErrorResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Error != string(test.code) ||
				response.Message != validationErr.SafeMessage ||
				response.Temporary != test.temporary ||
				response.RetryAfterSeconds != test.retryAfter {
				t.Fatalf("response = %#v", response)
			}
			if test.retryAfter > 0 {
				if got := recorder.Header().Get("Retry-After"); got != "17" {
					t.Fatalf("Retry-After = %q", got)
				}
			} else if got := recorder.Header().Get("Retry-After"); got != "" {
				t.Fatalf("unexpected Retry-After = %q", got)
			}
		})
	}
}

func TestOAuthEgressPolicyErrorHasStableSafeMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{
			name:   "egress policy",
			err:    oauthsvc.ErrEgressPolicy,
			status: http.StatusUnprocessableEntity,
			code:   "egress_policy_rejected",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			const providerSecret = "provider-owned-secret"
			e := echo.New()
			recorder := httptest.NewRecorder()
			context := e.NewContext(
				httptest.NewRequest(http.MethodPost, "/", nil),
				recorder,
			)
			if err := mapServiceError(
				context,
				errors.Join(test.err, errors.New(providerSecret)),
			); err != nil {
				t.Fatal(err)
			}
			var response ErrorResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != test.status || response.Error != test.code {
				t.Fatalf(
					"status=%d response=%#v",
					recorder.Code,
					response,
				)
			}
			if strings.Contains(recorder.Body.String(), providerSecret) {
				t.Fatalf("provider error leaked: %s", recorder.Body)
			}
		})
	}
}

func TestCredentialValidationErrorRejectsUnsafeMetadataShape(t *testing.T) {
	t.Parallel()
	echoServer := echo.New()
	recorder := httptest.NewRecorder()
	context := echoServer.NewContext(
		httptest.NewRequest(http.MethodPost, "/v1/connections/api-key", nil),
		recorder,
	)

	err := mapServiceError(context, &connector.CredentialValidationError{
		Code:           connector.FailureAuthorizationFailed,
		SafeMessage:    "must not be returned",
		UpstreamStatus: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body)
	}
	var response ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != "internal" ||
		response.Message != "internal error" {
		t.Fatalf("response = %#v", response)
	}
}
