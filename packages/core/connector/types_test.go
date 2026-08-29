package connector_test

import (
	"testing"

	"github.com/felinics/connect-it/packages/core/connector"
)

func TestDefinitionMode(t *testing.T) {
	remote := connector.Definition{Implementation: connector.RemoteMCP{}}
	managed := connector.Definition{Implementation: connector.Managed{}}
	if remote.Mode() != connector.ModeRemoteMCP || managed.Mode() != connector.ModeManaged {
		t.Fatalf("remote=%q managed=%q", remote.Mode(), managed.Mode())
	}
	if (connector.Definition{}).Mode() != "" {
		t.Fatal("zero-value definition should have no mode")
	}
}
