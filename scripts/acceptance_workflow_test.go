package scripts_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestContainedAcceptanceWorkflowIncludesEveryLocalCase(t *testing.T) {
	workflow, err := os.ReadFile("../.github/workflows/test.yml")
	if err != nil {
		t.Fatal(err)
	}
	command := regexp.MustCompile(`(?m)^\s+run: TF_ACC=1 go test -v -run '([^']+)' -count=1 ./internal/provider/$`)
	match := command.FindSubmatch(workflow)
	if len(match) != 2 {
		t.Fatal("contained acceptance command not found")
	}
	selection, err := regexp.Compile(string(match[1]))
	if err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir("../internal/provider")
	if err != nil {
		t.Fatal(err)
	}
	localCases := 0
	for _, entry := range files {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join("../internal/provider", entry.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv != nil || !strings.HasPrefix(function.Name.Name, "TestAcc") {
				continue
			}
			name := function.Name.Name
			local := strings.HasSuffix(name, "Local")
			if local {
				localCases++
			}
			if selection.MatchString(name) != local {
				t.Errorf("contained selection mismatch for %s: local=%t", name, local)
			}
		}
	}
	if localCases == 0 {
		t.Fatal("no contained acceptance cases found")
	}
}
