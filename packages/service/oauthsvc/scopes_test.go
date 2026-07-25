package oauthsvc

import (
	"reflect"
	"testing"
)

// RFC 6749 只把「省略 scope」定义为等同于请求的 scope。显式的空 scope 是已知的
// 空集合，一旦回退到 requested 就会静默放大授权。
func TestInitialGrantedScopesNeverWidensAnExplicitEmptyGrant(t *testing.T) {
	omitted, err := initialGrantedScopes(
		TokenValue{},
		[]string{"write", "read", "write"},
	)
	if err != nil || !reflect.DeepEqual(omitted, []string{"read", "write"}) {
		t.Fatalf("omitted scope fallback = %#v err=%v", omitted, err)
	}
	empty, err := initialGrantedScopes(
		TokenValue{ScopesKnown: true},
		[]string{"requested"},
	)
	if err != nil || len(empty) != 0 {
		t.Fatalf("explicit empty scope = %#v err=%v", empty, err)
	}
}
