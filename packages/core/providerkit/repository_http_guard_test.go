package providerkit_test

import (
	"bytes"
	"fmt"
	"go/ast"
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

const (
	ioImportPath     = "io"
	ioUtilImportPath = "io/ioutil"
	httpImportPath   = "net/http"

	clientValueRule    policyRule = "bare http.Client"
	transportValueRule policyRule = "bare http.Transport"
	unboundedReadRule  policyRule = "unbounded io.ReadAll"
	limitedMCPReadRule policyRule = "io.ReadAll(limited)"
)

type policyRule string

type policyException struct {
	path     string
	function string
	rule     policyRule
	reason   string
}

// Exceptions are exact reviewed sites, not package-wide exemptions. Counts
// make a second construction/read beside a reviewed one fail the gate.
var reviewedPolicyExceptions = []policyException{
	{
		path:     "packages/core/providerkit/rawclient.go",
		function: "(*Client).HTTPClient",
		rule:     clientValueRule,
		reason:   "streaming adapter with a fixed guarded transport",
	},
	{
		path:     "packages/core/providerkit/transport.go",
		function: "newGuardedTransport",
		rule:     transportValueRule,
		reason:   "single transport factory enforcing network policy",
	},
	{
		path:     "packages/api/mcp_ingress.go",
		function: "(*mcpIngress).limitBody",
		rule:     limitedMCPReadRule,
		reason:   "the named reader is an http.MaxBytesReader also closed on timeout",
	},
}

type policyViolation struct {
	path    string
	line    int
	message string
}

func (violation policyViolation) String() string {
	if violation.line == 0 {
		return fmt.Sprintf("%s: %s", violation.path, violation.message)
	}
	return fmt.Sprintf("%s:%d: %s", violation.path, violation.line, violation.message)
}

type repositoryHTTPGuard struct {
	exceptions []policyException
	uses       [][]int
	violations []policyViolation
}

func newRepositoryHTTPGuard(exceptions []policyException) *repositoryHTTPGuard {
	return &repositoryHTTPGuard{
		exceptions: append([]policyException(nil), exceptions...),
		uses:       make([][]int, len(exceptions)),
	}
}

func TestRepositoryProductionHTTPPolicy(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	guard := newRepositoryHTTPGuard(reviewedPolicyExceptions)
	if err := guard.scanRepository(root); err != nil {
		t.Fatal(err)
	}
	if violations := guard.finish(); len(violations) != 0 {
		t.Fatalf(
			"production HTTP policy violations; provider I/O must use "+
				"providerkit and io.ReadAll inputs must be bounded:\n  - %s",
			strings.Join(violationStrings(violations), "\n  - "),
		)
	}
}

func TestRepositoryHTTPGuardRejectsBypasses(t *testing.T) {
	tests := []struct {
		name, source, want string
		count              int
	}{
		{
			name: "aliased defaults",
			source: `package sample
import web "net/http"
var _ = web.DefaultClient
var _ = web.DefaultTransport
var _ = web.Get
`,
			count: 3,
			want:  "http.Get uses http.DefaultClient",
		},
		{
			name: "bare values constructors and type aliases",
			source: `package sample
import web "net/http"
type hidden = web.Client
var zero web.Transport
func constructors() {
	_ = &web.Client{}
	_ = new(web.Transport)
}
`,
			count: 4,
			want:  "bare http.Client",
		},
		{
			name: "dot imports",
			source: `package sample
import (
	. "io"
	. "io/ioutil"
	. "net/http"
)
`,
			count: 3,
			want:  "dot-import of net/http",
		},
		{
			name: "read aliases and unbounded reads",
			source: `package sample
import (
	stream "io"
	legacy "io/ioutil"
)
var readAll = stream.ReadAll
func read(body stream.Reader) {
	_, _ = stream.ReadAll(body)
	_, _ = legacy.ReadAll(body)
}
`,
			count: 3,
			want:  "io.ReadAll input is not syntactically bounded",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			guard := newRepositoryHTTPGuard(nil)
			if err := guard.checkFile(
				"packages/sample/sample.go",
				[]byte(test.source),
			); err != nil {
				t.Fatal(err)
			}
			violations := guard.finish()
			if len(violations) != test.count {
				t.Fatalf("violations = %v, want %d", violations, test.count)
			}
			report := strings.Join(violationStrings(violations), "\n")
			if !strings.Contains(report, test.want) {
				t.Fatalf("violations = %q, want substring %q", report, test.want)
			}
		})
	}
}

func TestRepositoryHTTPGuardChecksExactExceptions(t *testing.T) {
	exceptions := []policyException{{
		path:     "packages/core/providerkit/reviewed.go",
		function: "reviewed",
		rule:     clientValueRule,
		reason:   "fixture",
	}, {
		path:     "packages/sample/reviewed.go",
		function: "reviewed",
		rule:     limitedMCPReadRule,
		reason:   "fixture",
	}}
	sources := []struct{ path, source string }{
		{
			"packages/core/providerkit/reviewed.go",
			`package providerkit
import ("io"; "net/http")
func reviewed(body io.Reader) *http.Client {
	_, _ = io.ReadAll(io.LimitReader(body, 1024))
	return &http.Client{}
}`,
		}, {
			"packages/sample/reviewed.go",
			`package sample
import "io"
func reviewed(limited io.Reader) { _, _ = io.ReadAll(limited) }`,
		},
	}
	guard := newRepositoryHTTPGuard(exceptions)
	for _, source := range sources {
		if err := guard.checkFile(source.path, []byte(source.source)); err != nil {
			t.Fatal(err)
		}
	}
	if violations := guard.finish(); len(violations) != 0 {
		t.Fatalf("violations = %v", violations)
	}

	// Scanning the same approved sites again proves an extra occurrence cannot
	// hide beside an exception.
	for _, source := range sources {
		if err := guard.checkFile(source.path, []byte(source.source)); err != nil {
			t.Fatal(err)
		}
	}
	report := strings.Join(violationStrings(guard.finish()), "\n")
	if strings.Count(report, "expected exactly 1") != 2 ||
		!strings.Contains(report, "lines [") {
		t.Fatalf("exact exception count was not enforced: %s", report)
	}
}

func (guard *repositoryHTTPGuard) scanRepository(root string) error {
	packagesRoot := filepath.Join(root, "packages")
	return filepath.WalkDir(packagesRoot, func(
		path string,
		entry fs.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "dist", "generated", "node_modules", "testdata", "vendor":
				return filepath.SkipDir
			default:
				return nil
			}
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if isGeneratedSource(source) {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("make %s relative to %s: %w", path, root, err)
		}
		return guard.checkFile(filepath.ToSlash(relative), source)
	})
}

func isGeneratedSource(source []byte) bool {
	if len(source) > 2048 {
		source = source[:2048]
	}
	return bytes.Contains(source, []byte("Code generated")) &&
		bytes.Contains(source, []byte("DO NOT EDIT"))
}

func (guard *repositoryHTTPGuard) checkFile(path string, source []byte) error {
	files := token.NewFileSet()
	file, err := parser.ParseFile(files, path, source, parser.AllErrors)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	parents := astParents(file)
	imports, err := guard.imports(path, file, files)
	if err != nil {
		return err
	}

	ast.Inspect(file, func(node ast.Node) bool {
		switch expression := node.(type) {
		case *ast.SelectorExpr:
			guard.checkSelector(path, file, files, parents, imports, expression)
		case *ast.CallExpr:
			guard.checkReadAll(path, file, files, imports, expression)
		}
		return true
	})
	return nil
}

func (guard *repositoryHTTPGuard) imports(
	path string,
	file *ast.File,
	files *token.FileSet,
) (map[string]string, error) {
	imports := make(map[string]string)
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, fmt.Errorf("parse import path in %s: %w", path, err)
		}
		name := filepath.Base(importPath)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		switch name {
		case "_":
			continue
		case ".":
			if importPath == ioImportPath ||
				importPath == ioUtilImportPath ||
				importPath == httpImportPath {
				guard.add(
					path,
					files.Position(spec.Pos()).Line,
					fmt.Sprintf(
						"dot-import of %s is forbidden because it bypasses the HTTP guard",
						importPath,
					),
				)
			}
		default:
			imports[name] = importPath
		}
	}
	return imports, nil
}

func (guard *repositoryHTTPGuard) checkSelector(
	path string,
	file *ast.File,
	files *token.FileSet,
	parents map[ast.Node]ast.Node,
	imports map[string]string,
	selector *ast.SelectorExpr,
) {
	line := files.Position(selector.Pos()).Line
	switch selectorKey(selector, imports) {
	case httpImportPath + ".DefaultClient":
		guard.add(path, line, "http.DefaultClient is forbidden in production code")
	case httpImportPath + ".DefaultTransport":
		guard.add(path, line, "http.DefaultTransport is forbidden in production code")
	case httpImportPath + ".Get",
		httpImportPath + ".Head",
		httpImportPath + ".Post",
		httpImportPath + ".PostForm":
		guard.add(
			path,
			line,
			fmt.Sprintf(
				"http.%s uses http.DefaultClient and is forbidden in production code",
				selector.Sel.Name,
			),
		)
	case ioUtilImportPath + ".ReadAll":
		guard.add(path, line, "io/ioutil.ReadAll is forbidden in production code")
	case ioImportPath + ".ReadAll":
		call, direct := parents[selector].(*ast.CallExpr)
		if !direct || call.Fun != selector {
			guard.add(
				path,
				line,
				"io.ReadAll must be called directly so its bound can be checked",
			)
		}
	case httpImportPath + ".Client", httpImportPath + ".Transport":
		if !isPointerTypeUse(selector, parents) {
			rule := clientValueRule
			if selector.Sel.Name == "Transport" {
				rule = transportValueRule
			}
			guard.enforce(
				path,
				enclosingFunction(file, selector.Pos()),
				rule,
				line,
				fmt.Sprintf(
					"bare http.%s value or construction is forbidden; use providerkit",
					selector.Sel.Name,
				),
			)
		}
	}
}

func (guard *repositoryHTTPGuard) checkReadAll(
	path string,
	file *ast.File,
	files *token.FileSet,
	imports map[string]string,
	call *ast.CallExpr,
) {
	if selectorKey(call.Fun, imports) != ioImportPath+".ReadAll" {
		return
	}
	if len(call.Args) == 1 && isDirectlyBounded(call.Args[0], imports) {
		return
	}
	rule := unboundedReadRule
	if len(call.Args) == 1 {
		if identifier, ok := unparenthesize(call.Args[0]).(*ast.Ident); ok {
			rule = policyRule("io.ReadAll(" + identifier.Name + ")")
		}
	}
	guard.enforce(
		path,
		enclosingFunction(file, call.Pos()),
		rule,
		files.Position(call.Pos()).Line,
		"io.ReadAll input is not syntactically bounded; use io.LimitReader "+
			"or http.MaxBytesReader directly, or review one exact exception",
	)
}

func (guard *repositoryHTTPGuard) enforce(
	path string,
	function string,
	rule policyRule,
	line int,
	message string,
) {
	for index, exception := range guard.exceptions {
		if exception.path == path &&
			exception.function == function &&
			exception.rule == rule {
			guard.uses[index] = append(guard.uses[index], line)
			return
		}
	}
	guard.add(path, line, message)
}

func (guard *repositoryHTTPGuard) add(path string, line int, message string) {
	guard.violations = append(
		guard.violations,
		policyViolation{path: path, line: line, message: message},
	)
}

func (guard *repositoryHTTPGuard) finish() []policyViolation {
	violations := append([]policyViolation(nil), guard.violations...)
	for index, exception := range guard.exceptions {
		if (exception.rule == clientValueRule ||
			exception.rule == transportValueRule) &&
			!strings.HasPrefix(exception.path, "packages/core/providerkit/") {
			violations = append(violations, policyViolation{
				path: exception.path,
				message: "bare HTTP value exceptions are restricted to " +
					"packages/core/providerkit",
			})
		}
		if exception.reason == "" {
			violations = append(violations, policyViolation{
				path: exception.path,
				message: fmt.Sprintf(
					"%s in %s has no review reason",
					exception.rule,
					exception.function,
				),
			})
		}
		if len(guard.uses[index]) != 1 {
			violations = append(violations, policyViolation{
				path: exception.path,
				message: fmt.Sprintf(
					"%s in %s: expected exactly 1 reviewed occurrence, "+
						"found %d at lines %v (%s)",
					exception.rule,
					exception.function,
					len(guard.uses[index]),
					guard.uses[index],
					exception.reason,
				),
			})
		}
	}
	return violations
}

func selectorKey(expression ast.Expr, imports map[string]string) string {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	qualifier, ok := selector.X.(*ast.Ident)
	if !ok || qualifier.Obj != nil {
		return ""
	}
	return imports[qualifier.Name] + "." + selector.Sel.Name
}

func isDirectlyBounded(expression ast.Expr, imports map[string]string) bool {
	call, ok := unparenthesize(expression).(*ast.CallExpr)
	if !ok {
		return false
	}
	key := selectorKey(call.Fun, imports)
	return key == ioImportPath+".LimitReader" ||
		key == httpImportPath+".MaxBytesReader"
}

func unparenthesize(expression ast.Expr) ast.Expr {
	for {
		parenthesized, ok := expression.(*ast.ParenExpr)
		if !ok {
			return expression
		}
		expression = parenthesized.X
	}
}

func isPointerTypeUse(
	expression *ast.SelectorExpr,
	parents map[ast.Node]ast.Node,
) bool {
	pointer, ok := parents[expression].(*ast.StarExpr)
	return ok && pointer.X == expression
}

func astParents(root ast.Node) map[ast.Node]ast.Node {
	parents := make(map[ast.Node]ast.Node)
	var stack []ast.Node
	ast.Inspect(root, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if len(stack) != 0 {
			parents[node] = stack[len(stack)-1]
		}
		stack = append(stack, node)
		return true
	})
	return parents
}

func enclosingFunction(file *ast.File, position token.Pos) string {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil ||
			position < function.Body.Pos() ||
			position > function.Body.End() {
			continue
		}
		if function.Recv == nil || len(function.Recv.List) == 0 {
			return function.Name.Name
		}
		return fmt.Sprintf(
			"(%s).%s",
			receiverName(function.Recv.List[0].Type),
			function.Name.Name,
		)
	}
	return "package scope"
}

func receiverName(expression ast.Expr) string {
	switch receiver := expression.(type) {
	case *ast.Ident:
		return receiver.Name
	case *ast.StarExpr:
		return "*" + receiverName(receiver.X)
	case *ast.IndexExpr:
		return receiverName(receiver.X)
	case *ast.IndexListExpr:
		return receiverName(receiver.X)
	default:
		return "unknown"
	}
}

func repositoryRoot() (string, error) {
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("resolve repository guard source path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(sourcePath), "..", "..", ".."))
	for _, relative := range []string{
		"packages/core/go.mod",
		"packages/api/go.mod",
	} {
		info, err := os.Stat(filepath.Join(root, relative))
		if err != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("repository root marker %s is missing", relative)
		}
	}
	return root, nil
}

func violationStrings(violations []policyViolation) []string {
	result := make([]string, len(violations))
	for index, violation := range violations {
		result[index] = violation.String()
	}
	return result
}
