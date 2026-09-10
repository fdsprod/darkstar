// Command goreadability enforces multiline Go control/function bodies and one
// statement per line. It parses syntax, so braces/semicolons inside strings and
// comments are never interpreted as code. Use -fix for source-preserving fixes.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

type finding struct {
	position token.Position
	message  string
}

func inspect(name string, source []byte) ([]finding, []int, error) {
	positions := token.NewFileSet()
	file, err := parser.ParseFile(positions, name, source, parser.ParseComments)
	if err != nil {
		return nil, nil, err
	}
	var findings []finding
	edits := map[int]bool{}
	add := func(position token.Pos, message string) {
		location := positions.Position(position)
		findings = append(findings, finding{position: location, message: message})
		edits[location.Offset] = true
	}
	line := func(position token.Pos) int {
		return positions.Position(position).Line
	}
	statements := func(list []ast.Stmt) {
		for i := 1; i < len(list); i++ {
			if line(list[i-1].End()) == line(list[i].Pos()) {
				add(list[i].Pos(), "put each statement on its own line")
			}
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch body := node.(type) {
		case *ast.BlockStmt:
			if len(body.List) == 0 {
				return true
			}
			if line(body.Lbrace) == line(body.List[0].Pos()) {
				add(body.Lbrace+1, "start the block body on a new line")
			}
			if line(body.List[len(body.List)-1].End()) == line(body.Rbrace) {
				add(body.Rbrace, "put the closing brace on a separate line")
			}
			statements(body.List)
		case *ast.CaseClause:
			if len(body.Body) > 0 && line(body.Colon) == line(body.Body[0].Pos()) {
				add(body.Colon+1, "start the case body on a new line")
			}
			statements(body.Body)
		case *ast.CommClause:
			if len(body.Body) > 0 && line(body.Colon) == line(body.Body[0].Pos()) {
				add(body.Colon+1, "start the select case body on a new line")
			}
			statements(body.Body)
		}
		return true
	})
	offsets := make([]int, 0, len(edits))
	for offset := range edits {
		offsets = append(offsets, offset)
	}
	sort.Ints(offsets)
	return findings, offsets, nil
}

func fix(source []byte, offsets []int) ([]byte, error) {
	var result bytes.Buffer
	previous := 0
	for _, offset := range offsets {
		result.Write(source[previous:offset])
		result.WriteByte('\n')
		previous = offset
	}
	result.Write(source[previous:])
	updated, err := format.Source(result.Bytes())
	if err != nil {
		return nil, err
	}
	if !equalTokens(source, updated) {
		return nil, fmt.Errorf("readability formatting changed Go tokens")
	}
	if !equalSyntax(source, updated) {
		return nil, fmt.Errorf("readability formatting changed Go syntax")
	}
	return updated, nil
}

// Ignore whitespace and optional semicolons; equalSyntax checks their meaning.
// Comment and literal token contents (including embedded fixtures) must match.
func equalTokens(left, right []byte) bool {
	tokens := func(source []byte) []string {
		positions := token.NewFileSet()
		file := positions.AddFile("source.go", positions.Base(), len(source))
		var lexer scanner.Scanner
		lexer.Init(file, source, nil, scanner.ScanComments)
		var result []string
		for {
			_, kind, literal := lexer.Scan()
			if kind == token.EOF {
				sort.Strings(result) // gofmt may reorder existing import declarations.
				return result
			}
			if kind == token.SEMICOLON {
				continue
			}
			result = append(result, kind.String()+":"+literal)
		}
	}
	a, b := tokens(left), tokens(right)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalSyntax(left, right []byte) bool {
	syntax := func(source []byte) []byte {
		positions := token.NewFileSet()
		file, err := parser.ParseFile(positions, "source.go", source, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil
		}
		ast.SortImports(positions, file)
		var result bytes.Buffer
		_ = ast.Fprint(&result, nil, file, func(_ string, value reflect.Value) bool {
			return value.Type() != reflect.TypeFor[token.Pos]()
		})
		return result.Bytes()
	}
	a, b := syntax(left), syntax(right)
	return a != nil && b != nil && bytes.Equal(a, b)
}

func main() {
	write := flag.Bool("fix", false, "expand compact blocks and statements in place")
	flag.Parse()
	root := "."
	if flag.NArg() > 0 {
		root = flag.Arg(0)
	}
	failed := false
	files, issues := 0, 0
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == "vendor" || strings.HasPrefix(entry.Name(), ".") && name != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		source, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		findings, offsets, err := inspect(name, source)
		if err != nil {
			return err
		}
		if len(findings) == 0 {
			return nil
		}
		files++
		issues += len(findings)
		if *write {
			updated, err := fix(source, offsets)
			if err != nil {
				return fmt.Errorf("format %s: %w", name, err)
			}
			if err := os.WriteFile(name, updated, 0600); err != nil {
				return err
			}
			fmt.Println(name)
			return nil
		}
		failed = true
		for _, finding := range findings {
			fmt.Printf("%s: %s\n", finding.position, finding.message)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("Go readability: %d issue(s) in %d file(s).\n", issues, files)
	if failed {
		os.Exit(1)
	}
}
