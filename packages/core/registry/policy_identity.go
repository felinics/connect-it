package registry

import (
	"fmt"
	"slices"
	"sort"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

// PolicyIdentityField is a compiled, machine-checkable field declaration.
type PolicyIdentityField struct {
	Key    string
	Secret bool
}

// ConnectorPolicyIdentitySpec is the immutable Registry projection consumed by
// configsvc's pure identity projector. Version changes only for an incompatible
// identity serialization contract.
type ConnectorPolicyIdentitySpec struct {
	Version int
	Fields  []PolicyIdentityField
}

const connectorPolicyIdentityVersion = 2

// PolicyIdentitySpec derives the small immutable projection from the stored
// Definition. Keeping a second registry cache would create another fact source.
func (r *Registry) PolicyIdentitySpec(t connector.Type) (ConnectorPolicyIdentitySpec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.defs[t]
	if !ok {
		return ConnectorPolicyIdentitySpec{}, false
	}
	return compileConnectorPolicyIdentitySpec(def), true
}

func validatePolicyIdentityDefinition(def connector.Definition) error {
	fields := make(map[string]connector.ConfigField, len(def.ConfigFields))
	for _, field := range def.ConfigFields {
		fields[field.Key] = field
	}

	for _, server := range def.RemoteMCPServers {
		if server.Endpoint.Source != connector.EndpointConfigField {
			continue
		}
		field, ok := fields[server.Endpoint.ConfigFieldKey]
		if !ok {
			// The ordinary Registry endpoint validation reports this with more
			// context; keep this guard for standalone testability.
			continue
		}
		if field.Secret || field.InputType != connector.InputURL {
			return fmt.Errorf(
				"connector %q: MCP server %q endpoint 字段 %q 必须是非 Secret InputURL",
				def.Type,
				server.Key,
				field.Key,
			)
		}
	}

	if field, ok := fields["allow_insecure_http"]; ok {
		if err := validateInsecureHTTPField(
			def.Type,
			"ConfigFields",
			field,
		); err != nil {
			return err
		}
	}
	for _, method := range def.AuthMethods {
		for _, field := range method.CredentialFields {
			if field.Key != "allow_insecure_http" {
				continue
			}
			if err := validateInsecureHTTPField(
				def.Type,
				fmt.Sprintf("auth method %q CredentialFields", method.Key),
				field,
			); err != nil {
				return err
			}
		}
	}
	if field, ok := fields["client_id"]; ok && field.Secret {
		return fmt.Errorf("connector %q: OAuth client_id 不能声明为 Secret", def.Type)
	}

	for _, method := range def.AuthMethods {
		if method.Type != connector.AuthOAuth2 || method.OAuth == nil {
			continue
		}
		endpoints := map[string]string{
			"AuthorizationEndpoint": method.OAuth.AuthorizationEndpoint,
			"TokenEndpoint":         method.OAuth.TokenEndpoint,
			"RefreshTokenEndpoint":  method.OAuth.RefreshTokenEndpoint,
		}
		for label, endpoint := range endpoints {
			if !providerkit.ValidEndpointTemplate(endpoint) {
				return fmt.Errorf(
					"connector %q: auth method %q 的 %s 含非法 placeholder",
					def.Type,
					method.Key,
					label,
				)
			}
			for _, key := range providerkit.EndpointPlaceholders(endpoint) {
				field, ok := fields[key]
				if !ok {
					return fmt.Errorf(
						"connector %q: auth method %q 的 %s 引用不存在配置字段 %q",
						def.Type,
						method.Key,
						label,
						key,
					)
				}
				if field.Secret {
					return fmt.Errorf(
						"connector %q: auth method %q 的 %s 不能引用 Secret 字段 %q",
						def.Type,
						method.Key,
						label,
						key,
					)
				}
			}
		}
	}
	return nil
}

func validateInsecureHTTPField(
	connectorType connector.Type,
	owner string,
	field connector.ConfigField,
) error {
	if field.Secret ||
		field.InputType != connector.InputSelect ||
		field.DefaultValue == nil ||
		*field.DefaultValue != "false" ||
		!sameStringSet(field.Validation.Options, []string{"false", "true"}) {
		return fmt.Errorf(
			"connector %q: %s 的 allow_insecure_http 必须是非 Secret InputSelect，"+
				"default=false 且 Options 精确为 false/true",
			connectorType,
			owner,
		)
	}
	return nil
}

func compileConnectorPolicyIdentitySpec(def connector.Definition) ConnectorPolicyIdentitySpec {
	fields := make(map[string]connector.ConfigField, len(def.ConfigFields))
	included := make(map[string]struct{}, len(def.ConfigFields))
	add := func(key string) {
		if _, ok := fields[key]; !ok {
			return
		}
		included[key] = struct{}{}
	}
	for _, field := range def.ConfigFields {
		fields[field.Key] = field
		if field.PolicyIdentity {
			add(field.Key)
		}
	}
	for _, server := range def.RemoteMCPServers {
		if server.Endpoint.Source == connector.EndpointConfigField {
			add(server.Endpoint.ConfigFieldKey)
		}
	}
	for _, key := range []string{"allow_insecure_http", "network_mode"} {
		add(key)
	}
	for _, method := range def.AuthMethods {
		if method.Type != connector.AuthOAuth2 || method.OAuth == nil {
			continue
		}
		endpoints := []string{
			method.OAuth.AuthorizationEndpoint,
			method.OAuth.TokenEndpoint,
			method.OAuth.RefreshTokenEndpoint,
		}
		add("client_id")
		for _, endpoint := range endpoints {
			for _, key := range providerkit.EndpointPlaceholders(endpoint) {
				add(key)
			}
		}
	}

	keys := make([]string, 0, len(included))
	for key := range included {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	spec := ConnectorPolicyIdentitySpec{
		Version: connectorPolicyIdentityVersion,
		Fields:  make([]PolicyIdentityField, 0, len(keys)),
	}
	for _, key := range keys {
		spec.Fields = append(spec.Fields, PolicyIdentityField{
			Key:    key,
			Secret: fields[key].Secret,
		})
	}
	return spec
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	return slices.Equal(left, right)
}
