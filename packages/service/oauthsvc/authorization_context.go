package oauthsvc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/service/store"
)

const authorizationContextVersion = 1

// authorizationContext is encrypted before an authorization row is written.
// It binds every value that may influence callback verification or outbound
// endpoint selection to the state, attempt and generation that will claim it.
type authorizationContext struct {
	Version                         int
	ConnectorType                   connector.Type
	AuthMethodKey                   string
	StateHash                       string
	AttemptVersion                  int64
	ExpectedAuthorizationGeneration int64
	PKCEVerifier                    string
}

type authorizationContextJSON struct {
	Version                         int            `json:"version"`
	ConnectorType                   connector.Type `json:"connector_type"`
	AuthMethodKey                   string         `json:"auth_method_key"`
	StateHash                       string         `json:"state_hash"`
	AttemptVersion                  int64          `json:"attempt_version"`
	ExpectedAuthorizationGeneration int64          `json:"expected_authorization_generation"`
	PKCEVerifier                    string         `json:"pkce_verifier,omitempty"`
}

func encryptAuthorizationContext(
	kr *crypto.Keyring,
	authorizationID uuid.UUID,
	value authorizationContext,
) ([]byte, int, error) {
	if kr == nil || authorizationID == uuid.Nil {
		return nil, 0, errors.New("oauthsvc: invalid authorization context")
	}
	if err := validateAuthorizationContext(value); err != nil {
		return nil, 0, err
	}
	plaintext, err := json.Marshal(authorizationContextJSON{
		Version:                         value.Version,
		ConnectorType:                   value.ConnectorType,
		AuthMethodKey:                   value.AuthMethodKey,
		StateHash:                       value.StateHash,
		AttemptVersion:                  value.AttemptVersion,
		ExpectedAuthorizationGeneration: value.ExpectedAuthorizationGeneration,
		PKCEVerifier:                    value.PKCEVerifier,
	})
	if err != nil {
		return nil, 0, errors.New("oauthsvc: encode authorization context")
	}
	return kr.Encrypt(plaintext, authorizationContextAAD(authorizationID))
}

func decryptAuthorizationContext(
	kr *crypto.Keyring,
	row store.OauthAuthorization,
) (authorizationContext, error) {
	if kr == nil ||
		row.ID == uuid.Nil ||
		row.ContextVersion != authorizationContextVersion {
		return authorizationContext{}, ErrInvalidState
	}
	plaintext, err := kr.Decrypt(
		row.ContextCiphertext,
		int(row.SecretKeyVersion),
		authorizationContextAAD(row.ID),
	)
	if err != nil {
		return authorizationContext{}, ErrInvalidState
	}
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	var payload authorizationContextJSON
	if err := decoder.Decode(&payload); err != nil {
		return authorizationContext{}, ErrInvalidState
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return authorizationContext{}, ErrInvalidState
	}
	value := authorizationContext{
		Version:                         payload.Version,
		ConnectorType:                   payload.ConnectorType,
		AuthMethodKey:                   payload.AuthMethodKey,
		StateHash:                       payload.StateHash,
		AttemptVersion:                  payload.AttemptVersion,
		ExpectedAuthorizationGeneration: payload.ExpectedAuthorizationGeneration,
		PKCEVerifier:                    payload.PKCEVerifier,
	}
	if err := validateAuthorizationContext(value); err != nil {
		return authorizationContext{}, ErrInvalidState
	}
	if value.ConnectorType != connector.Type(row.ConnectorType) ||
		value.AuthMethodKey != row.AuthMethod ||
		value.StateHash != row.StateHash ||
		value.AttemptVersion != row.AttemptVersion ||
		value.ExpectedAuthorizationGeneration !=
			row.ExpectedAuthorizationGeneration {
		return authorizationContext{}, ErrInvalidState
	}
	return value, nil
}

func validateAuthorizationContext(value authorizationContext) error {
	if value.Version != authorizationContextVersion {
		return fmt.Errorf("oauthsvc: unsupported authorization context version")
	}
	if value.ConnectorType == "" ||
		value.AuthMethodKey == "" ||
		value.StateHash == "" ||
		value.AttemptVersion < 1 ||
		value.ExpectedAuthorizationGeneration < 1 {
		return errors.New("oauthsvc: incomplete authorization context")
	}
	return nil
}

func authorizationContextAAD(id uuid.UUID) []byte {
	return []byte("connect-it/authorization-context/v1/" + id.String())
}
