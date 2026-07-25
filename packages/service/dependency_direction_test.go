package service

import (
	"os/exec"
	"strings"
	"testing"
)

func TestServiceDoesNotDependOnConcreteProviders(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-deps", "./...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps ./...: %v\n%s", err, out)
	}
	const connectorsModule = "github.com/memohai/connect-it/packages/connectors"
	for _, dependency := range strings.Fields(string(out)) {
		if dependency == connectorsModule || strings.HasPrefix(dependency, connectorsModule+"/") {
			t.Fatalf("service 不得依赖具体 Provider module: %s", dependency)
		}
	}
}
