package core_test

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	coreModule       = "github.com/memohai/connect-it/packages/core"
	jsonSchemaModule = "github.com/google/jsonschema-go"
)

func TestCoreDeclaresJSONSchemaAsDirectDependency(t *testing.T) {
	t.Parallel()

	root := coreModuleRoot(t)
	cmd := exec.Command("go", "mod", "edit", "-json")
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	var mod struct {
		Module struct {
			Path string
		}
		Require []struct {
			Path     string
			Indirect bool
		}
	}
	if err := json.Unmarshal(output, &mod); err != nil {
		t.Fatalf("decode go.mod: %v", err)
	}
	if mod.Module.Path != coreModule {
		t.Fatalf("module path = %q, want %q", mod.Module.Path, coreModule)
	}
	for _, requirement := range mod.Require {
		if requirement.Path == jsonSchemaModule {
			if requirement.Indirect {
				t.Fatalf("%s must be a direct dependency", jsonSchemaModule)
			}
			return
		}
	}
	t.Fatalf("go.mod must directly require %s", jsonSchemaModule)
}

func TestCoreProductionImportsDoNotPointUpward(t *testing.T) {
	t.Parallel()

	root := coreModuleRoot(t)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "vendor":
				return filepath.SkipDir
			default:
				return nil
			}
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return fmt.Errorf("parse import in %s: %w", path, err)
			}
			if isStandardLibrary(importPath) ||
				importPath == coreModule ||
				strings.HasPrefix(importPath, coreModule+"/") ||
				importPath == jsonSchemaModule ||
				strings.HasPrefix(importPath, jsonSchemaModule+"/") {
				continue
			}
			return fmt.Errorf("%s imports forbidden higher-layer or unreviewed dependency %q", path, importPath)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func coreModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func isStandardLibrary(importPath string) bool {
	first, _, _ := strings.Cut(importPath, "/")
	return !strings.Contains(first, ".")
}
