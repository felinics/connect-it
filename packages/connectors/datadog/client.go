package datadog

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/memohai/connect-it/packages/connectors/internal/credentialvalidator"
	"github.com/memohai/connect-it/packages/connectors/internal/restkit"
	"github.com/memohai/connect-it/packages/connectors/internal/toolfail"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

const (
	datadogAuthMethod          = "api_keys"
	datadogAPIKeyField         = "api_key"
	datadogApplicationKeyField = "application_key"
	datadogSiteField           = "site"
	datadogMaxResponseBytes    = 10 << 20
	datadogMaxQueryWindowSecs  = 31 * 24 * 60 * 60
)

// datadogFailureRemap is Datadog's own HTTP-status semantics. Every other
// status keeps providerkit's classification, and 401 always becomes the
// explicit credential-invalid signal.
var datadogFailureRemap = map[int]connector.FailureCode{
	http.StatusBadRequest:          connector.FailureInvalidInput,
	http.StatusUnprocessableEntity: connector.FailureInvalidInput,
	http.StatusRequestTimeout:      connector.FailureTimeout,
}

// datadogSites is deliberately closed. A credential selects one reviewed
// Datadog site and can never provide an arbitrary hostname.
var datadogSites = map[string]string{
	"us1":     "https://api.datadoghq.com:443/",
	"us3":     "https://api.us3.datadoghq.com:443/",
	"us5":     "https://api.us5.datadoghq.com:443/",
	"eu":      "https://api.datadoghq.eu:443/",
	"ap1":     "https://api.ap1.datadoghq.com:443/",
	"ap2":     "https://api.ap2.datadoghq.com:443/",
	"uk1":     "https://api.uk1.datadoghq.com:443/",
	"us1_fed": "https://api.ddog-gov.com:443/",
	"us2_fed": "https://api.us2.ddog-gov.com:443/",
}

type datadogCredentials struct {
	apiKey         string
	applicationKey string
	site           string
}

type datadogClientSource struct {
	clients map[string]*providerkit.Client
}

func newDatadogClientSource(
	factory *providerkit.Factory,
) (*datadogClientSource, error) {
	if factory == nil {
		return nil, fmt.Errorf("datadog: provider client factory is required")
	}
	clients := make(map[string]*providerkit.Client, len(datadogSites))
	for site, baseURL := range datadogSites {
		client, err := factory.NewStaticClient(providerkit.Policy{
			Provider:         string(Definition.Type),
			BaseURL:          baseURL,
			AllowedOrigins:   []string{strings.TrimSuffix(baseURL, "/")},
			RedirectMode:     providerkit.RedirectDenyAll,
			NetworkMode:      providerkit.PublicOnly,
			RequestTimeout:   providerkit.DefaultRequestTimeout,
			MaxResponseBytes: datadogMaxResponseBytes,
			Retry:            providerkit.DefaultRetryPolicy(),
		})
		if err != nil {
			return nil, err
		}
		clients[site] = client
	}
	return &datadogClientSource{clients: clients}, nil
}

// transport declares the one guarded egress path shared by every Datadog Tool.
func (source *datadogClientSource) transport() restkit.Transport[datadogCredentials] {
	return restkit.Transport[datadogCredentials]{
		Connector: Definition.Type,
		Credentials: func(
			call connector.ToolCallContext,
		) (datadogCredentials, *connector.ToolFailure) {
			credentials, code := credentialsFromToolCall(call)
			return credentials, toolfail.Code(code)
		},
		Authorizer: func(
			credentials datadogCredentials,
		) (providerkit.Authorizer, *connector.ToolFailure) {
			authorizer, code := credentials.authorizer()
			return authorizer, toolfail.Code(code)
		},
		Client:       source.clientFor,
		Headers:      http.Header{"Accept": {"application/json"}},
		FailureRemap: datadogFailureRemap,
	}
}

// clientFor leases the reviewed static Client for the credential's site. A nil
// failure means the lease is usable.
func (source *datadogClientSource) clientFor(
	credentials datadogCredentials,
) (restkit.Lease, *connector.ToolFailure) {
	if source == nil || source.clients[credentials.site] == nil {
		return restkit.Lease{},
			toolfail.New(connector.FailureConfigurationError, 0, 0)
	}
	// The Client is a long-lived reviewed static one, so the lease must not
	// take ownership of its connection pool.
	return restkit.Shared(source.clients[credentials.site]), nil
}

func credentialsFromToolCall(
	call connector.ToolCallContext,
) (datadogCredentials, connector.FailureCode) {
	fields, ok := credentialvalidator.ToolFields(
		call,
		Definition.Type,
		datadogAPIKeyField,
		datadogApplicationKeyField,
		datadogSiteField,
	)
	if !ok {
		return datadogCredentials{}, connector.FailureConfigurationError
	}
	return normalizeDatadogCredentials(fields)
}

func normalizeDatadogCredentials(
	fields map[string]string,
) (datadogCredentials, connector.FailureCode) {
	apiKey := fields[datadogAPIKeyField]
	applicationKey := fields[datadogApplicationKeyField]
	if !validDatadogSecret(apiKey) || !validDatadogSecret(applicationKey) {
		return datadogCredentials{},
			connector.FailureAuthorizationFailed
	}
	site := fields[datadogSiteField]
	if _, ok := datadogSites[site]; !ok {
		return datadogCredentials{},
			connector.FailureConfigurationError
	}
	return datadogCredentials{
		apiKey:         apiKey,
		applicationKey: applicationKey,
		site:           site,
	}, ""
}

func validDatadogSecret(value string) bool {
	return credentialvalidator.OpaqueSecret(value)
}

// datadogAuthorizer declares both secret headers in one complete credential
// footprint. Redirect and observability code therefore strips/redacts the pair
// together.
type datadogAuthorizer struct {
	apiKey         string
	applicationKey string
}

func (authorizer datadogAuthorizer) Apply(request *http.Request) error {
	if request == nil || !validDatadogSecret(authorizer.apiKey) ||
		!validDatadogSecret(authorizer.applicationKey) {
		return providerkit.ErrInvalidAuthorizerConfig
	}
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	request.Header.Set("DD-API-KEY", authorizer.apiKey)
	request.Header.Set("DD-APPLICATION-KEY", authorizer.applicationKey)
	return nil
}

func (datadogAuthorizer) Footprint() providerkit.CredentialFootprint {
	return providerkit.CredentialFootprint{
		HeaderNames: []string{"DD-API-KEY", "DD-APPLICATION-KEY"},
	}
}

func (credentials datadogCredentials) authorizer() (
	providerkit.Authorizer,
	connector.FailureCode,
) {
	if !validDatadogSecret(credentials.apiKey) ||
		!validDatadogSecret(credentials.applicationKey) {
		return nil, connector.FailureAuthorizationFailed
	}
	return datadogAuthorizer{
		apiKey:         credentials.apiKey,
		applicationKey: credentials.applicationKey,
	}, ""
}
