// Package googleoauth provides the shared OAuth definition used by Google
// connectors. Each connector still declares its own scopes and implementation.
package googleoauth

import "github.com/felinics/connect-it/packages/core/connector"

func ConfigFields() []connector.ConfigField {
	return []connector.ConfigField{
		{
			Key:         "client_id",
			Label:       "Client ID",
			InputType:   connector.InputText,
			Required:    true,
			Description: "Client ID of the Google Cloud OAuth 2.0 client.",
		},
		{
			Key:         "client_secret",
			Label:       "Client Secret",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "Client secret of the Google Cloud OAuth 2.0 client.",
		},
	}
}

func Method(scopes ...string) connector.AuthMethod {
	return connector.AuthMethod{
		Key:   "oauth",
		Type:  connector.AuthOAuth2,
		Label: "Google OAuth",
		OAuth: &connector.OAuthConfig{
			AuthorizationEndpoint: "https://accounts.google.com/o/oauth2/v2/auth",
			TokenEndpoint:         "https://oauth2.googleapis.com/token",
			Scopes:                scopes,
			TokenEndpointAuth:     connector.TokenAuthPost,
			ExtraAuthParams: map[string]string{
				"access_type": "offline",
				"prompt":      "consent",
			},
		},
	}
}
