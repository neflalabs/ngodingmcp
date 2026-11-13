package workspace

import (
	"fmt"
	"strings"
)

// checkDelimiters is a light, dependency-free check for C-like languages
// (JavaScript, TypeScript, CSS). It does not parse the language; it only
// verifies that (), [] and {} are balanced and that strings, template
// literals and block comments are closed. That catches the most common
// breakage left behind by a bad edit.
//
// jsLike enables JavaScript rules: ' " ` strings, // comments, ${} inside
// template literals and regular-expression literals. Without it (CSS) only
// ' " strings and /* */ comments are recognised.
func checkDelimiters(src string, jsLike bool) []SyntaxError {
	type open struct {
		ch        byte
		line, col int
	}
	var stack []open
	line, col := 1, 0
	// prev is the last significant (non-space, non-comment) byte, used to
	// decide whether '/' starts a regex literal or is a division.
	var prev byte
	prevWord := ""

	errAt := func(l, c int, format string, args ...any) []SyntaxError {
		return []SyntaxError{{Line: l, Col: c, Message: fmt.Sprintf(format, args...)}}
	}

	n := len(src)
	for i := 0; i < n; i++ {
		c := src[i]
		if c == '\n' {
			line++
			col = 0
			continue
		}
		col++
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			continue

		case c == '/' && i+1 < n && src[i+1] == '*':
			sl, sc := line, col
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return errAt(sl, sc, "unterminated block comment")
			}
			seg := src[i : i+2+end+2]
			line, col = advance(seg, line, col-1)
			i += len(seg) - 1
			continue

		case jsLike && c == '/' && i+1 < n && src[i+1] == '/':
			for i+1 < n && src[i+1] != '\n' {
				i++
			}
			continue

		case c == '"' || c == '\'' || (jsLike && c == '`'):
			sl, sc := line, col
			j, l2, c2, ok := skipString(src, i, line, col, jsLike)
			if !ok {
				return errAt(sl, sc, "unterminated string starting with %c", c)
			}
			i, line, col = j, l2, c2
			prev, prevWord = c, ""
			continue

		case jsLike && c == '/' && regexAllowed(prev, prevWord):
			sl, sc := line, col
			j, ok := skipRegex(src, i)
			if !ok {
				return errAt(sl, sc, "unterminated regular expression")
			}
			col += j - i
			i = j
			prev, prevWord = '/', ""
			continue

		case c == '(' || c == '[' || c == '{':
			stack = append(stack, open{c, line, col})

		case c == ')' || c == ']' || c == '}':
			want := map[byte]byte{')': '(', ']': '[', '}': '{'}[c]
			if len(stack) == 0 {
				return errAt(line, col, "unexpected %c with no matching opener", c)
			}
			top := stack[len(stack)-1]
			if top.ch != want {
				return errAt(line, col, "unexpected %c; %c opened at line %d, col %d is not closed", c, top.ch, top.line, top.col)
			}
			stack = stack[:len(stack)-1]
		}

		if isIdentByte(c) {
			if isIdentByte(prev) && prevWord != "" {
				prevWord += string(c)
			} else {
				prevWord = string(c)
			}
		} else {
			prevWord = ""
		}
		prev = c
	}
	if len(stack) > 0 {
		top := stack[len(stack)-1]
		return errAt(top.line, top.col, "%c is never closed", top.ch)
	}
	return nil
}

// skipString moves past the string literal that starts at src[i]. For
// template literals it also checks ${ ... } substitutions recursively. It
// returns the index of the closing quote and the updated line/col.
func skipString(src string, i, line, col int, jsLike bool) (int, int, int, bool) {
	q := src[i]
	for j := i + 1; j < len(src); j++ {
		c := src[j]
		if c == '\n' {
			if q != '`' {
				return 0, 0, 0, false
			}
			line++
			col = 0
			continue
		}
		col++
		switch {
		case c == '\\':
			if j+1 < len(src) && src[j+1] == '\n' {
				line++
				col = 0
			} else {
				col++
			}
			j++
		case c == q:
			return j, line, col, true
		case q == '`' && c == '$' && j+1 < len(src) && src[j+1] == '{':
			end, ok := matchSubstitution(src, j+1)
			if !ok {
				return 0, 0, 0, false
			}
			line, col = advance(src[j+1:end+1], line, col)
			j = end
		}
	}
	return 0, 0, 0, false
}

// matchSubstitution finds the } that closes the ${ starting at src[open].
func matchSubstitution(src string, open int) (int, bool) {
	depth := 0
	for j := open; j < len(src); j++ {
		switch c := src[j]; c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return j, true
			}
		case '"', '\'', '`':
			end, _, _, ok := skipString(src, j, 0, 0, true)
			if !ok {
				return 0, false
			}
			j = end
		}
	}
	return 0, false
}

// skipRegex moves past a regex literal starting at src[i] and its flags.
func skipRegex(src string, i int) (int, bool) {
	inClass := false
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\n':
			return 0, false
		case '\\':
			j++
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if !inClass {
				for j+1 < len(src) && isIdentByte(src[j+1]) {
					j++
				}
				return j, true
			}
		}
	}
	return 0, false
}

// regexAllowed reports whether a '/' after prev starts a regex literal.
func regexAllowed(prev byte, word string) bool {
	switch word {
	case "return", "typeof", "instanceof", "in", "of", "new", "delete", "void", "throw", "case", "do", "else", "yield", "await":
		return true
	}
	if prev == 0 {
		return true
	}
	return strings.IndexByte("(,=:[!&|?{};+-*%<>~^", prev) >= 0
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// advance moves line/col across seg, which starts right after column col.
func advance(seg string, line, col int) (int, int) {
	for i := 0; i < len(seg); i++ {
		if seg[i] == '\n' {
			line++
			col = 0
		} else {
			col++
		}
	}
	return line, col
}
