package credentialvalidator

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
	"github.com/memohai/connect-it/packages/core/providerkit/testkit"
)

// A misconfigured Probe must fail closed before any credential leaves the
// process; the request path is exercised end to end by the Provider packages.
func TestRunRejectsMisconfiguredProbeWithoutRequest(t *testing.T) {
	var calls atomic.Int32
	server := testkit.NewServerWithOptions(
		t,
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			calls.Add(1)
			t.Error("misconfigured probe reached the Provider")
		}),
		testkit.ServerOptions{Provider: "probe"},
	)
	project := func(struct{}) (connector.CredentialValidationResult, error) {
		t.Error("Project ran for a misconfigured probe")
		return connector.CredentialValidationResult{}, nil
	}
	tests := map[string]struct {
		client *providerkit.Client
		probe  Probe[struct{}]
	}{
		"nil client": {
			probe: Probe[struct{}]{Method: http.MethodGet, Project: project},
		},
		"nil projection": {
			client: server.Client,
			probe:  Probe[struct{}]{Method: http.MethodGet},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Run(
				t.Context(),
				test.client,
				test.probe,
				providerkit.RequestLabels{},
			)
			var validationErr *connector.CredentialValidationError
			if !errors.As(err, &validationErr) ||
				validationErr.Code != connector.FailureConfigurationError {
				t.Fatalf("error = %#v (%v)", validationErr, err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("misconfigured probes made %d requests", calls.Load())
	}
}
