package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"
)

func TestCompactBodiesAndStatementsAreExpanded(t *testing.T) {
	source := []byte(`package sample
func empty() {}
func work(ok bool) { if ok { first(); second() }; defer empty(); empty() }
func cases(n int, c chan int) { switch n { case 1: first(); second(); default: empty() }; select { case <-c: empty(); default: empty() } }
func callback() { _ = func() { first(); second() } }
`)
	findings, offsets, err := inspect("sample.go", source)
	if err != nil || len(findings) == 0 {
		t.Fatalf("compact sample was not rejected: %v, %v", findings, err)
	}
	updated, err := fix(source, offsets)
	if err != nil {
		t.Fatal(err)
	}
	repeated, _, err := inspect("sample.go", updated)
	if err != nil || len(repeated) != 0 {
		t.Fatalf("fix left findings: %v, %v\n%s", repeated, err, updated)
	}
	if !reflect.DeepEqual(structure(t, source), structure(t, updated)) {
		t.Fatalf("formatting changed syntax structure:\n%s", updated)
	}
}

func TestStringsCommentsAndControlHeadersAreNotStatements(t *testing.T) {
	source := []byte("package sample\nfunc sample() {\n" +
		"// if x { first(); second() }\n" +
		"text := `func f() { a(); b() }`\n" +
		"_ = text\n" +
		"for i := 0; i < 2; i++ {\n_ = i\n}\n" +
		"if value := len(text); value > 0 {\n_ = value\n}\n" +
		"_ = struct{ Value string }{Value: \"{;}\"}\n}\n")
	findings, _, err := inspect("sample.go", source)
	if err != nil || len(findings) != 0 {
		t.Fatalf("valid multiline code was rejected: %v, %v", findings, err)
	}
}

func TestParsingErrorsFailClosed(t *testing.T) {
	if _, _, err := inspect("invalid.go", []byte("package sample\nfunc broken(")); err == nil {
		t.Fatal("invalid Go silently passed")
	}
}

func TestEquivalenceRejectsChangedStatementsAndLiteralContents(t *testing.T) {
	before := []byte("package sample\nfunc f() { first(); second() }\n")
	reordered := []byte("package sample\nfunc f() { second(); first() }\n")
	if equalSyntax(before, reordered) {
		t.Fatal("syntax equivalence ignored statement ordering")
	}
	if equalTokens([]byte("package p; var x = `first`"), []byte("package p; var x = `second`")) {
		t.Fatal("token equivalence ignored literal contents")
	}
}

// Structure compares node kinds and literal/identifier values while excluding
// source positions, which the formatter intentionally changes.
func structure(t *testing.T, source []byte) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "sample.go", source, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	var result []string
	ast.Inspect(file, func(node ast.Node) bool {
		if node == nil {
			return true
		}
		result = append(result, reflect.TypeOf(node).String())
		switch value := node.(type) {
		case *ast.Ident:
			result = append(result, value.Name)
		case *ast.BasicLit:
			result = append(result, value.Value)
		case *ast.Comment:
			result = append(result, value.Text)
		}
		return true
	})
	return result
}
