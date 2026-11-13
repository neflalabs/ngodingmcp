package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newExtrasWS(t *testing.T) *Workspace {
	t.Helper()
	ws, err := New(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return ws
}

func writeExtras(t *testing.T, ws *Workspace, rel, body string) {
	t.Helper()
	full := filepath.Join(ws.Root(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIsSensitive(t *testing.T) {
	cases := map[string]bool{
		".env":               true,
		".env.local":         true,
		".env.production":    true,
		".env.example":       false,
		"config/.ENV":        true,
		".git/config":        true,
		"a/.ssh/known_hosts": true,
		"certs/server.pem":   true,
		"keys/app.KEY":       true,
		"id_ed25519":         true,
		".npmrc":             true,
		"main.go":            false,
		"docs/keys.md":       false,
		"environment.go":     false,
		".gitignore":         false,
		".":                  false,
	}
	for rel, want := range cases {
		if got := IsSensitive(rel); got != want {
			t.Errorf("IsSensitive(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestSensitiveBlockingAndOverride(t *testing.T) {
	ws := newExtrasWS(t)
	writeExtras(t, ws, ".env", "TOKEN=1\n")
	writeExtras(t, ws, "app/main.go", "package main\n")
	writeExtras(t, ws, "app/.git/HEAD", "ref: refs/heads/main\n")

	if _, err := ws.ReadFile(".env", 0, 0); !errors.Is(err, ErrSensitive) {
		t.Fatalf("ReadFile(.env) err = %v, want ErrSensitive", err)
	}
	if _, _, err := ws.WriteFile(".env", "x"); !errors.Is(err, ErrSensitive) {
		t.Fatalf("WriteFile(.env) err = %v, want ErrSensitive", err)
	}
	if err := ws.Delete("app", true); !errors.Is(err, ErrSensitive) {
		t.Fatalf("recursive Delete over .git err = %v, want ErrSensitive", err)
	}

	entries, err := ws.ListFiles(".", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if IsSensitive(e.Path) {
			t.Errorf("listing exposed %s", e.Path)
		}
	}

	ws.SetAllowSensitive(true)
	if _, err := ws.ReadFile(".env", 0, 0); err != nil {
		t.Fatalf("with allow-sensitive, ReadFile(.env) err = %v", err)
	}
}

func TestSafeErrorHidesRoot(t *testing.T) {
	ws := newExtrasWS(t)
	_, err := ws.ReadFile("missing.txt", 0, 0)
	if err == nil {
		t.Fatal("expected an error")
	}
	safe := ws.SafeError(err)
	if strings.Contains(safe.Error(), ws.Root()) {
		t.Errorf("SafeError leaked root: %s", safe)
	}
	if !errors.Is(safe, os.ErrNotExist) && errors.Is(err, os.ErrNotExist) {
		t.Error("SafeError must keep errors.Is")
	}
	if got := ws.ScrubPaths(ws.Root() + string(filepath.Separator) + "a/b.go: boom"); got != "a/b.go: boom" {
		t.Errorf("ScrubPaths = %q", got)
	}
}

func TestCheckDelimiters(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		js    bool
		valid bool
	}{
		{"balanced", "function f(a) { return [a, {b: 1}]; }\n", true, true},
		{"brace in string", "const s = '}'; const t = \"(\";\n", true, true},
		{"template", "const a = `x ${ {y: 1}.y } }`;\n", true, true},
		{"regex", "const r = /[)}]+/g;\nconst d = a / b / c;\n", true, true},
		{"comments", "// )\n/* ] */ let x = 1;\n", true, true},
		{"unclosed brace", "function f() {\n  return 1;\n", true, false},
		{"mismatch", "call(a, [b);\n", true, false},
		{"unterminated string", "const s = 'abc;\n", true, false},
		{"unterminated comment", "/* never ends\n", true, false},
		{"css ok", "a { color: red; } @media (x) { b { c: d } }\n", false, true},
		{"css broken", "a { color: red;\n", false, false},
	}
	for _, c := range cases {
		errs := checkDelimiters(c.src, c.js)
		if (len(errs) == 0) != c.valid {
			t.Errorf("%s: valid=%v, errors=%v", c.name, len(errs) == 0, errs)
		}
	}
}

func TestValidateSyntaxCheckField(t *testing.T) {
	ws := newExtrasWS(t)
	writeExtras(t, ws, "a.go", "package a\n")
	writeExtras(t, ws, "b.css", "a { b: c }\n")
	writeExtras(t, ws, "c.md", "# hi\n")
	for rel, want := range map[string]string{"a.go": "parser", "b.css": "delimiters", "c.md": "none"} {
		res, err := ws.ValidateSyntax(rel)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if res.Check != want || !res.Valid {
			t.Errorf("%s: check=%q valid=%v, want %q valid", rel, res.Check, res.Valid, want)
		}
	}
}

func TestEditFileAtomicAndDiff(t *testing.T) {
	ws := newExtrasWS(t)
	writeExtras(t, ws, "x.txt", "a\nb\nc\nb\n")

	if _, err := ws.EditFile("x.txt", []Edit{{Old: "b", New: "B"}}, false); err == nil {
		t.Fatal("non-unique old text must fail without replace_all")
	}
	res, err := ws.EditFile("x.txt", []Edit{{Old: "b", New: "B", ReplaceAll: true}, {Old: "c", New: "C"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Replacements != 3 || !strings.Contains(res.Diff, "+B") || !strings.Contains(res.Diff, "-c") {
		t.Errorf("unexpected result: %+v", res)
	}
	b, _ := os.ReadFile(filepath.Join(ws.Root(), "x.txt"))
	if string(b) != "a\nB\nC\nB\n" {
		t.Errorf("content = %q", b)
	}
	if _, err := ws.EditFile(".env", []Edit{{Old: "a", New: "b"}}, false); !errors.Is(err, ErrSensitive) && !errors.Is(err, os.ErrNotExist) {
		t.Errorf("edit of .env err = %v", err)
	}
}
