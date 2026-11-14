package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// SyntaxError holds a single parse error with its location.
type SyntaxError struct {
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	Message string `json:"message"`
}

// SyntaxResult is returned by ValidateSyntax.
type SyntaxResult struct {
	Path     string        `json:"path"`
	Language string        `json:"language"`
	Valid    bool          `json:"valid"`
	Errors   []SyntaxError `json:"errors"`
	// Check says how deep the check went: "parser" (full parse),
	// "delimiters" (brackets, strings and comments only) or "none".
	Check string `json:"check,omitempty"`
}

// ValidateSyntax checks the syntax of the file at rel.
// Full parse: .go (via go/parser), .json (via encoding/json).
// Delimiter check (balanced brackets, closed strings/comments):
// .js/.mjs/.cjs, .ts/.mts/.cts and .css.
// .yaml/.yml returns valid=true with language="yaml" and check="none".
// Everything else returns valid=true with language="unknown" and check="none".
func (w *Workspace) ValidateSyntax(rel string) (SyntaxResult, error) {
	full, err := w.resolve(rel)
	if err != nil {
		return SyntaxResult{}, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return SyntaxResult{}, err
	}
	if info.IsDir() {
		return SyntaxResult{}, ErrIsDir
	}
	if info.Size() > w.MaxRead() {
		return SyntaxResult{}, fmt.Errorf("%w: %d bytes exceeds limit %d", ErrTooLarge, info.Size(), w.MaxRead())
	}
	relp := w.relPath(full)
	ext := strings.ToLower(filepath.Ext(full))

	switch ext {
	case ".go":
		return validateGo(full, relp)
	case ".json":
		return validateJSON(full, relp)
	case ".js", ".mjs", ".cjs":
		return validateDelimiters(full, relp, "javascript", true)
	case ".ts", ".mts", ".cts":
		return validateDelimiters(full, relp, "typescript", true)
	case ".css":
		return validateDelimiters(full, relp, "css", false)
	case ".yaml", ".yml":
		return SyntaxResult{Path: relp, Language: "yaml", Valid: true, Errors: nil, Check: "none"}, nil
	default:
		return SyntaxResult{Path: relp, Language: "unknown", Valid: true, Errors: nil, Check: "none"}, nil
	}
}

// validateDelimiters runs the lightweight delimiter check for C-like
// languages. JSX/TSX are left out on purpose: text such as <p>it's</p>
// would be reported as an unterminated string.
func validateDelimiters(full, relp, lang string, jsLike bool) (SyntaxResult, error) {
	data, err := os.ReadFile(full)
	if err != nil {
		return SyntaxResult{}, err
	}
	if looksBinary(data) {
		return SyntaxResult{}, ErrBinary
	}
	errs := checkDelimiters(string(data), jsLike)
	return SyntaxResult{Path: relp, Language: lang, Valid: len(errs) == 0, Errors: errs, Check: "delimiters"}, nil
}

func validateGo(full, relp string) (SyntaxResult, error) {
	fset := token.NewFileSet()
	_, err := parser.ParseFile(fset, full, nil, 0)
	if err == nil {
		return SyntaxResult{Path: relp, Language: "go", Valid: true, Errors: nil, Check: "parser"}, nil
	}
	var errs []SyntaxError
	if el, ok := err.(scanner.ErrorList); ok {
		for _, e := range el {
			errs = append(errs, SyntaxError{
				Line:    e.Pos.Line,
				Col:     e.Pos.Column,
				Message: e.Msg,
			})
		}
	} else {
		errs = append(errs, SyntaxError{Line: 0, Col: 0, Message: err.Error()})
	}
	return SyntaxResult{Path: relp, Language: "go", Valid: false, Errors: errs, Check: "parser"}, nil
}

func validateJSON(full, relp string) (SyntaxResult, error) {
	data, err := os.ReadFile(full)
	if err != nil {
		return SyntaxResult{}, err
	}
	if looksBinary(data) {
		return SyntaxResult{}, ErrBinary
	}
	var v any
	if err := json.Unmarshal(data, &v); err == nil {
		return SyntaxResult{Path: relp, Language: "json", Valid: true, Errors: nil, Check: "parser"}, nil
	} else {
		var syntaxErr *json.SyntaxError
		var se SyntaxError
		if errors.As(err, &syntaxErr) {
			// compute line number from offset
			offset := int(syntaxErr.Offset)
			if offset > len(data) {
				offset = len(data)
			}
			line := 1 + strings.Count(string(data[:offset]), "\n")
			se = SyntaxError{Line: line, Col: 0, Message: syntaxErr.Error()}
		} else {
			se = SyntaxError{Line: 0, Col: 0, Message: err.Error()}
		}
		return SyntaxResult{Path: relp, Language: "json", Valid: false, Errors: []SyntaxError{se}, Check: "parser"}, nil
	}
}

// --- count_tokens ----------------------------------------------------------
