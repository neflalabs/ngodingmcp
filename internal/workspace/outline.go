package workspace

// Source outlines: top-level symbols with line ranges, used by the outline and
// read_symbol tools so an AI can jump to one declaration instead of reading a
// whole file. Go uses go/ast; JavaScript and TypeScript use a line-based
// fallback (the plan only promises a simple regex there).

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
)

// Symbol is one top-level declaration found by Outline.
type Symbol struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"` // func, method, type, const, var, class
	Start int    `json:"startLine"`
	End   int    `json:"endLine"`
}

// Outline is the symbol list of one file.
type Outline struct {
	Path       string   `json:"path"`
	Language   string   `json:"language"`
	TotalLines int      `json:"totalLines"`
	Symbols    []Symbol `json:"symbols"`
	// Hash identifies the whole file, so outline and read_symbol can accept
	// if_changed_since like read_file does.
	Hash string `json:"hash"`
}

// Outline lists top-level symbols in rel. Unsupported languages return an
// empty list with Language set; the tool explains how to fall back.
func (w *Workspace) Outline(rel string) (Outline, error) {
	full, content, err := w.readAll(rel)
	if err != nil {
		return Outline{}, err
	}
	lang := sourceLanguage(full)
	out := Outline{
		Path:       w.relPath(full),
		Language:   lang,
		TotalLines: countLines(content),
		Symbols:    []Symbol{},
		Hash:       contentHash(content),
	}
	switch lang {
	case "go":
		syms, err := goOutline(full, content)
		if err != nil {
			return Outline{}, err
		}
		out.Symbols = syms
	case "javascript", "typescript":
		out.Symbols = jsOutline(content)
	}
	return out, nil
}

// ReadSymbol reads just the declaration named name, using the same ranges as
// Outline.
func (w *Workspace) ReadSymbol(rel, name string) (ReadResult, Symbol, error) {
	out, err := w.Outline(rel)
	if err != nil {
		return ReadResult{}, Symbol{}, err
	}
	for _, s := range out.Symbols {
		if s.Name != name {
			continue
		}
		res, err := w.ReadFile(rel, s.Start, s.End-s.Start+1)
		if err != nil {
			return ReadResult{}, Symbol{}, err
		}
		return res, s, nil
	}
	return ReadResult{}, Symbol{}, fmt.Errorf("symbol %q not found in %s; call outline to list symbols", name, out.Path)
}

func sourceLanguage(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".go":
		return "go"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".ts", ".tsx", ".mts", ".cts":
		return "typescript"
	default:
		return ""
	}
}

func goOutline(name, src string) ([]Symbol, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	if f == nil {
		return nil, err
	}
	var syms []Symbol
	add := func(n ast.Node, symName, kind string) {
		syms = append(syms, Symbol{
			Name:  symName,
			Kind:  kind,
			Start: fset.Position(n.Pos()).Line,
			End:   fset.Position(n.End()).Line,
		})
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv != nil && len(d.Recv.List) > 0 {
				add(d, receiverTypeName(d.Recv.List[0].Type)+"."+d.Name.Name, "method")
			} else {
				add(d, d.Name.Name, "func")
			}
		case *ast.GenDecl:
			switch d.Tok {
			case token.TYPE:
				for _, spec := range d.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						add(ts, ts.Name.Name, "type")
					}
				}
			case token.CONST, token.VAR:
				kind := "const"
				if d.Tok == token.VAR {
					kind = "var"
				}
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for _, n := range vs.Names {
						// One line range per name: grouped const/var blocks
						// usually put each name on its own spec.
						add(vs, n.Name, kind)
					}
				}
			}
		}
	}
	return syms, nil
}

func receiverTypeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return receiverTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return receiverTypeName(t.X)
	case *ast.IndexListExpr:
		return receiverTypeName(t.X)
	case *ast.ParenExpr:
		return receiverTypeName(t.X)
	default:
		return "?"
	}
}

// jsSymbolPatterns is a deliberately small fallback. It matches top-level-ish
// function, class, interface/type and arrow-function declarations; nested
// declarations are skipped by jumping to the end of the matched body.
var jsSymbolPatterns = []struct {
	re   *regexp.Regexp
	kind string
}{
	{regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s*\*?\s*([A-Za-z_$][\w$]*)\s*\(`), "func"},
	{regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:abstract\s+)?class\s+([A-Za-z_$][\w$]*)`), "class"},
	{regexp.MustCompile(`^\s*(?:export\s+)?(?:interface|enum)\s+([A-Za-z_$][\w$]*)`), "type"},
	{regexp.MustCompile(`^\s*(?:export\s+)?type\s+([A-Za-z_$][\w$]*)\s*=`), "type"},
	{regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*(?:async\s*)?(?:function\b|\([^)]*\)\s*=>|[A-Za-z_$][\w$]*\s*=>)`), "func"},
}

func jsOutline(src string) []Symbol {
	lines := strings.Split(strings.TrimSuffix(src, "\n"), "\n")
	syms := make([]Symbol, 0, 8)
	for i := 0; i < len(lines); i++ {
		for _, p := range jsSymbolPatterns {
			m := p.re.FindStringSubmatch(lines[i])
			if m == nil {
				continue
			}
			end := jsDeclarationEnd(lines, i)
			syms = append(syms, Symbol{Name: m[1], Kind: p.kind, Start: i + 1, End: end + 1})
			i = end
			break
		}
	}
	return syms
}

// jsDeclarationEnd returns the 0-based index of the last line of the
// declaration starting at start. It counts braces and skips same-line strings
// and comments; a declaration without a body ends on its first line.
func jsDeclarationEnd(lines []string, start int) int {
	depth := 0
	seen := false
	inBlockComment := false
	for i := start; i < len(lines); i++ {
		line := lines[i]
		for j := 0; j < len(line); j++ {
			c := line[j]
			if inBlockComment {
				if c == '*' && j+1 < len(line) && line[j+1] == '/' {
					inBlockComment = false
					j++
				}
				continue
			}
			if c == '/' && j+1 < len(line) {
				switch line[j+1] {
				case '/':
					j = len(line)
					continue
				case '*':
					inBlockComment = true
					j++
					continue
				}
			}
			switch c {
			case '"', '\'':
				j = skipJSString(line, j+1, c)
			case '`':
				j = skipJSString(line, j+1, '`')
			case '{':
				depth++
				seen = true
			case '}':
				if seen {
					depth--
					if depth == 0 {
						return i
					}
				}
			}
		}
	}
	if !seen {
		return start
	}
	return len(lines) - 1
}

func skipJSString(line string, i int, quote byte) int {
	for ; i < len(line); i++ {
		if line[i] == '\\' {
			i++
			continue
		}
		if line[i] == quote {
			return i
		}
	}
	return len(line) - 1
}
