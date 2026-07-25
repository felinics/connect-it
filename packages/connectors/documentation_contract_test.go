package connectors_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Provider documentation is operational guidance, not a second schema or
// release state machine. This test keeps only the useful repository contract:
// every registered Provider has exactly one source-linked document, and no
// document survives its Definition.
func TestRegisteredProviderDocumentation(t *testing.T) {
	root := repositoryRoot(t)
	docsDirectory := filepath.Join(root, "docs", "providers")
	expectedDocuments := make(map[string]bool)

	for _, definition := range newRegistry(t).All() {
		providerType := string(definition.Type)
		documentName := providerType + ".md"
		expectedDocuments[documentName] = true
		documentPath := filepath.Join(docsDirectory, documentName)
		document := readRegularFile(t, documentPath)

		requireDocumentFragment(
			t,
			providerType,
			document,
			"Connector type: `"+providerType+"`",
		)
		requireDocumentFragment(t, providerType, document, "## Smoke records")
		requireDocumentFragment(t, providerType, document, "## Upgrade notes")
		assertSourceGovernance(t, providerType, document)
		for _, tool := range definition.Tools {
			requireDocumentFragment(t, providerType, document, "`"+tool.ID+"`")
		}
	}

	entries, err := os.ReadDir(docsDirectory)
	if err != nil {
		t.Fatalf("read Provider documentation directory: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		if !expectedDocuments[entry.Name()] {
			t.Errorf(
				"Provider document %q has no registered Definition",
				entry.Name(),
			)
		}
		delete(expectedDocuments, entry.Name())
	}
	for name := range expectedDocuments {
		t.Errorf("registered Provider document %q is missing", name)
	}
}

func assertSourceGovernance(
	t *testing.T,
	providerType string,
	document string,
) {
	t.Helper()
	const heading = "\n## Source"
	start := strings.Index(document, heading)
	if start < 0 {
		t.Errorf("%s: documentation is missing a Source section", providerType)
		return
	}
	section := document[start+1:]
	if end := strings.Index(section, "\n## "); end >= 0 {
		section = section[:end]
	}

	for _, fragment := range []string{
		"https://",
		"oomol-lab/open-connector",
		"https://github.com/oomol-lab/open-connector",
		"Apache-2.0",
		"src/providers/",
	} {
		requireDocumentFragment(t, providerType, section, fragment)
	}
	if !strings.Contains(strings.ToLower(section), "official") {
		t.Errorf("%s: Source section must identify official documentation", providerType)
	}
	for label, pattern := range map[string]string{
		"source review date": `\b20[0-9]{2}-[0-9]{2}-[0-9]{2}\b`,
		"full commit SHA":    `\b[0-9a-fA-F]{40}\b`,
	} {
		if !regexp.MustCompile(pattern).MatchString(section) {
			t.Errorf("%s: Source section is missing %s", providerType, label)
		}
	}
}

func requireDocumentFragment(
	t *testing.T,
	providerType string,
	document string,
	fragment string,
) {
	t.Helper()
	if !strings.Contains(document, fragment) {
		t.Errorf("%s: documentation is missing %q", providerType, fragment)
	}
}

func readRegularFile(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("%s is not a regular file: %v", path, err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "pnpm-workspace.yaml")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("repository root not found")
		}
		directory = parent
	}
}
