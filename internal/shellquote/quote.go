// Package shellquote escapes values that are interpolated into a POSIX shell
// command line or into a generated shell script.
//
// Go's %q verb is not a shell escape: it produces a Go string literal, so when
// its result reaches a shell every metacharacter the shell understands still
// applies. Measured under bash (TASK-312): a path containing $ dies with
// "unbound variable", a path containing a backtick runs the command
// substitution for real, and Go's C-style escapes for control characters
// (\n, \t, \x01) stay literal two- and four-character sequences instead of the
// bytes they encode, so the interpolated path stops naming the file. Quote
// emits POSIX single-quoting instead, which sh, dash, bash and ksh all read
// back byte-for-byte.
package shellquote

import "strings"

// Quote returns s quoted so a POSIX shell treats it as one literal word.
//
// The result wraps s in single quotes and rewrites every embedded single quote
// as the POSIX close-escaped-quote-reopen idiom: quote, backslash, quote,
// quote. Inside single quotes the shell performs no parameter expansion,
// command substitution, globbing, tilde expansion or word splitting, so it
// reads back exactly s — including $, `, \, *, ?, ~, newlines and tabs. An
// empty s falls out as a pair of adjacent single quotes, an empty word, so it
// needs no separate branch.
//
// Quote always adds the surrounding quotes, even when s holds nothing the shell
// would touch: a caller that has to decide whether quoting is necessary is a
// caller that will eventually decide wrong.
//
// Do not use it for non-shell sinks. A SQL statement, a YAML document or a Go
// format string needs the quoting of its own language.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
