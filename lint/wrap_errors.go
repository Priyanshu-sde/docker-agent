package main

import (
	"go/ast"
	"go/token"
	"go/types"
	"strconv"

	"github.com/dgageot/rubocop-go/cop"
)

// WrapErrors flags fmt.Errorf calls that interpolate an error value without
// the %w verb.
//
// AGENTS.md requires errors to be wrapped with fmt.Errorf("...: %w", err) so
// that callers can errors.Is / errors.As through them; a %v or %s on an error
// flattens it to a string and breaks the chain. The errorlint linter (already
// enabled) checks comparison and type-assertion sites but does not inspect the
// formatting verb passed to fmt.Errorf, so this cop closes that gap.
//
// The rule only fires when an argument's type is exactly the built-in error
// interface, so passing a string field named Error (a common shape on API
// response types) is not mistaken for an error value. A format string that
// already contains %w is left alone — wrapping one error per call is enough
// to keep the chain intact.
//
// Per-line suppression: `//rubocop:disable Lint/WrapErrors`.
var WrapErrors = &cop.Func{
	Meta: cop.Meta{
		Name:        "Lint/WrapErrors",
		Description: "fmt.Errorf must wrap error values with %w, not %v/%s",
		Severity:    cop.Warning,
	},
	Types: true,
	Run: func(p *cop.Pass) {
		if p.Info == nil {
			return
		}
		p.ForEachCall(func(call *ast.CallExpr) {
			if !cop.IsCallTo(call, "fmt", "Errorf") || len(call.Args) < 2 {
				return
			}
			format, ok := stringLit(call.Args[0])
			if !ok {
				return
			}
			if hasWrapVerb(format) {
				return // already wrapping at least one error
			}
			for _, arg := range call.Args[1:] {
				if isErrorType(p.Info.TypeOf(arg)) {
					p.Report(call, "fmt.Errorf interpolates an error without %w — wrap it so errors.Is/As keep working")
					return
				}
			}
		})
	},
}

// hasWrapVerb reports whether format contains a real %w verb, ignoring
// escaped percent signs. A naive strings.Contains(format, "%w") would treat
// the literal "%%w" (an escaped percent followed by the letter w) as a
// wrapping verb and silence a genuinely unwrapped error.
//
// Flags, width, precision, and argument indices between the percent and the
// verb letter are skipped, so forms like %-10w, %[1]w, and %3.2w are also
// recognised as wrap verbs.
func hasWrapVerb(format string) bool {
	for i := 0; i < len(format); i++ {
		if format[i] != '%' || i+1 >= len(format) {
			continue
		}
		if format[i+1] == '%' {
			i++ // consume the escaped percent so "%%w" is not read as a verb
			continue
		}
		// Skip flags, width, precision, and [n] argument indices to reach
		// the verb letter that terminates the directive.
		j := i + 1
		for j < len(format) && !isLetter(format[j]) {
			j++
		}
		if j < len(format) && format[j] == 'w' {
			return true
		}
	}
	return false
}

// isLetter reports whether b is an ASCII letter, the set of bytes fmt uses
// for verbs.
func isLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// stringLit returns the unquoted value of a string-literal expression.
func stringLit(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	val, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return val, true
}

// isErrorType reports whether t is exactly the built-in error interface.
func isErrorType(t types.Type) bool {
	if t == nil {
		return false
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	return named.Obj() != nil && named.Obj().Name() == "error" && named.Obj().Pkg() == nil
}
