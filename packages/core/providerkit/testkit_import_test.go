package providerkit_test

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const providerTestkitImport = "github.com/memohai/connect-it/packages/core/providerkit/testkit"

func TestProviderTestkitIsNeverImportedByProductionCode(t *testing.T) {
	t.Parallel()

	repository := repositoryRootFromSource(t)
	err := filepath.WalkDir(
		repository,
		func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				switch entry.Name() {
				case ".git", "node_modules", "vendor", "dist":
					return filepath.SkipDir
				default:
					return nil
				}
			}
			if filepath.Ext(path) != ".go" ||
				strings.HasSuffix(path, "_test.go") {
				return nil
			}
			source, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			file, err := parser.ParseFile(
				token.NewFileSet(),
				path,
				source,
				parser.ImportsOnly,
			)
			if err != nil {
				return fmt.Errorf("parse %s: %w", path, err)
			}
			for _, spec := range file.Imports {
				importPath, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					return fmt.Errorf("parse import in %s: %w", path, err)
				}
				if importPath == providerTestkitImport ||
					strings.HasPrefix(
						importPath,
						providerTestkitImport+"/",
					) {
					return fmt.Errorf(
						"%s imports providerkit/testkit outside a test file",
						path,
					)
				}
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
}

// repositoryRootFromSource resolves the repository root from this file's own
// location (packages/core/providerkit), so the walk does not depend on the
// working directory the test binary happens to run in.
func repositoryRootFromSource(t *testing.T) string {
	t.Helper()
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve testkit import guard source path")
	}
	root := filepath.Clean(
		filepath.Join(filepath.Dir(sourcePath), "..", "..", ".."),
	)
	if _, err := os.Stat(filepath.Join(root, "packages", "core", "go.mod")); err != nil {
		t.Fatalf("repository root %s is missing packages/core/go.mod: %v", root, err)
	}
	return root
}
