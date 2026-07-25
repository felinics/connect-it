package connector

// AuthorizationRuntime is the complete set of provider-owned authorization
// implementations. Nil maps are valid and mean that no implementation of that
// kind is registered.
//
// Keeping this bundle in core gives composition roots and services one typed
// contract without making them import concrete provider packages.
type AuthorizationRuntime struct {
	CredentialValidators CredentialValidatorMap
	ScopeMatchers        ScopeMatcherMap
}
