package path

import (
	"os"
	"regexp"
)

// jsEnvRef matches the JS-template form `${env.VAR}` (optional surrounding
// whitespace). Shell-style callers (os.Expand / os.ExpandEnv) historically
// only understood `${VAR}`, so the JS form was passed through as a literal.
// NormalizeEnvRefs rewrites it to `${VAR}` so both syntaxes resolve
// identically.
//
// Only the plain `${env.VAR}` reference is recognized; richer JS expressions
// such as `${env.VAR || 'default'}` are left untouched (evaluating them would
// require the goja engine, which pkg/path cannot import).
var jsEnvRef = regexp.MustCompile(`\$\{\s*env\.([A-Za-z_][A-Za-z0-9_]*)\s*\}`)

// NormalizeEnvRefs rewrites the JS-template form `${env.VAR}` to the shell
// form `${VAR}`, so os.Expand-based callers also accept the JS-style syntax
// used elsewhere in the config (issue #2615). Richer JS expressions are left
// untouched; see jsEnvRef.
func NormalizeEnvRefs(s string) string {
	return jsEnvRef.ReplaceAllString(s, "${$1}")
}

// ExpandPath expands shell-like patterns in a file path:
//   - ~ or ~/ at the start is replaced with the user's home directory
//   - Environment variables like ${HOME} or $HOME are expanded
//   - The JS-template form ${env.HOME} is accepted as an alias for ${HOME}
func ExpandPath(p string) string {
	if p == "" {
		return p
	}

	// Normalize ${env.VAR} to ${VAR} so the JS-template syntax used elsewhere
	// in the config also works in path fields (issue #2615).
	p = NormalizeEnvRefs(p)

	// Expand environment variables
	p = os.ExpandEnv(p)

	if expanded, err := ExpandHomeDir(p); err == nil {
		return expanded
	}

	return p
}
