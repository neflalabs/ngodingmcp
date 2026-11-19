package workspace

import (
	"strings"
	"testing"
)

func TestOutlineGo(t *testing.T) {
	ws := newTestWorkspace(t)
	const src = `package demo

const Answer = 42

var (
	A = 1
	B = 2
)

type Server struct {
	Name string
}

func New() *Server {
	return &Server{}
}

func (s *Server) Run(n int) int {
	return n
}

func (s Server) Stop() {}
`
	mustWrite(t, ws, "demo.go", src)

	out, err := ws.Outline("demo.go")
	if err != nil {
		t.Fatal(err)
	}
	if out.Language != "go" || out.TotalLines != 22 {
		t.Fatalf("outline header = %q, %d lines", out.Language, out.TotalLines)
	}
	if out.Hash == "" {
		t.Error("outline has no hash")
	}
	want := []Symbol{
		{Name: "Answer", Kind: "const", Start: 3, End: 3},
		{Name: "A", Kind: "var", Start: 6, End: 6},
		{Name: "B", Kind: "var", Start: 7, End: 7},
		{Name: "Server", Kind: "type", Start: 10, End: 12},
		{Name: "New", Kind: "func", Start: 14, End: 16},
		{Name: "Server.Run", Kind: "method", Start: 18, End: 20},
		{Name: "Server.Stop", Kind: "method", Start: 22, End: 22},
	}
	if len(out.Symbols) != len(want) {
		t.Fatalf("symbols = %+v, want %d", out.Symbols, len(want))
	}
	for i, w := range want {
		if out.Symbols[i] != w {
			t.Errorf("symbol %d = %+v, want %+v", i, out.Symbols[i], w)
		}
	}
}

func TestReadSymbolGo(t *testing.T) {
	ws := newTestWorkspace(t)
	const src = `package demo

func First() int {
	return 1
}

func Second() int {
	return 2
}
`
	mustWrite(t, ws, "demo.go", src)

	res, sym, err := ws.ReadSymbol("demo.go", "Second")
	if err != nil {
		t.Fatal(err)
	}
	if sym.Start != 7 || sym.End != 9 {
		t.Fatalf("symbol = %+v", sym)
	}
	if !strings.Contains(res.Content, "func Second() int") || !strings.Contains(res.Content, "return 2") {
		t.Fatalf("content = %q", res.Content)
	}
	if strings.Contains(res.Content, "func First") {
		t.Fatalf("read_symbol returned the whole file: %q", res.Content)
	}
	if res.Hash == "" {
		t.Error("read_symbol result has no hash")
	}
	if _, _, err := ws.ReadSymbol("demo.go", "Missing"); err == nil {
		t.Fatal("missing symbol should fail")
	}
}

func TestOutlineJavaScript(t *testing.T) {
	ws := newTestWorkspace(t)
	const src = `// helpers
export function add(a, b) {
  return a + b;
}
class Counter {
  inc() { this.n++; }
}
export const mul = (a, b) => a * b;
`
	mustWrite(t, ws, "helpers.js", src)

	out, err := ws.Outline("helpers.js")
	if err != nil {
		t.Fatal(err)
	}
	if out.Language != "javascript" {
		t.Fatalf("language = %q", out.Language)
	}
	want := []Symbol{
		{Name: "add", Kind: "func", Start: 2, End: 4},
		{Name: "Counter", Kind: "class", Start: 5, End: 7},
		{Name: "mul", Kind: "func", Start: 8, End: 8},
	}
	if len(out.Symbols) != len(want) {
		t.Fatalf("symbols = %+v, want %d", out.Symbols, len(want))
	}
	for i, w := range want {
		if out.Symbols[i] != w {
			t.Errorf("symbol %d = %+v, want %+v", i, out.Symbols[i], w)
		}
	}
}

func TestOutlineUnsupported(t *testing.T) {
	ws := newTestWorkspace(t)
	mustWrite(t, ws, "notes.txt", "hello\n")

	out, err := ws.Outline("notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if out.Language != "" || len(out.Symbols) != 0 || out.TotalLines != 1 {
		t.Fatalf("outline = %+v", out)
	}
}

func TestReadFileHash(t *testing.T) {
	ws := newTestWorkspace(t)
	mustWrite(t, ws, "a.txt", "one\ntwo\n")

	first, err := ws.ReadFile("a.txt", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ws.ReadFile("a.txt", 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.Hash == "" {
		t.Fatal("empty hash")
	}
	if first.Hash != second.Hash {
		t.Fatalf("hash depends on the slice: %q != %q", first.Hash, second.Hash)
	}

	mustWrite(t, ws, "a.txt", "one\ntwo\nthree\n")
	changed, err := ws.ReadFile("a.txt", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Hash == first.Hash {
		t.Fatal("hash did not change with the content")
	}
}
