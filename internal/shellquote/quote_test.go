package shellquote

import (
	"fmt"
	"strings"
	"testing"
)

// tableCases is the shared corpus: the literal expectations and the round-trip
// property are both checked against it, so a case can never be added to one
// side only.
func tableCases() []struct {
	name string
	in   string
	want string
} {
	return []struct {
		name string
		in   string
		want string
	}{
		{"plain path", "/srv/release-manager/deploy/dev/dev.sh", "'/srv/release-manager/deploy/dev/dev.sh'"},
		{"spaces", "/srv/release manager/dev.sh", "'/srv/release manager/dev.sh'"},
		{"dollar", "/srv/a$b/c.sh", "'/srv/a$b/c.sh'"},
		{"command substitution", "/srv/a`id`b/c.sh", "'/srv/a`id`b/c.sh'"},
		{"backslash", `/srv/a\b/c.sh`, `'/srv/a\b/c.sh'`},
		{"two backslashes", `/srv/a\\b/c.sh`, `'/srv/a\\b/c.sh'`},
		{"single quote", "/srv/it's/c.sh", `'/srv/it'\''s/c.sh'`},
		{"only a single quote", "'", `''\'''`},
		{"only two single quotes", "''", `''\'''\'''`},
		{"newline", "/srv/a\nb/c.sh", "'/srv/a\nb/c.sh'"},
		{"tab", "/srv/a\tb/c.sh", "'/srv/a\tb/c.sh'"},
		{"carriage return", "a\rb", "'a\rb'"},
		{"newline next to a quote", "a'b\nc", "'a'\\''b\nc'"},
		{"glob star", "/srv/a*b/c.sh", "'/srv/a*b/c.sh'"},
		{"glob question mark", "/srv/a?b/c.sh", "'/srv/a?b/c.sh'"},
		{"glob bracket", "/srv/a[bc]d/c.sh", "'/srv/a[bc]d/c.sh'"},
		{"tilde", "~/dev.sh", "'~/dev.sh'"},
		{"leading dash", "--force", "'--force'"},
		{"double quote", `/srv/a"b/c.sh`, `'/srv/a"b/c.sh'`},
		{"semicolon and pipe", "/srv/a;b|c/c.sh", "'/srv/a;b|c/c.sh'"},
		{"empty", "", "''"},
		{"unicode path", "/srv/路径/脚本.sh", "'/srv/路径/脚本.sh'"},
		{"combining mark and emoji", "e\u0301\U0001F642", "'e\u0301\U0001F642'"},
	}
}

func TestQuote(t *testing.T) {
	for _, tt := range tableCases() {
		t.Run(tt.name, func(t *testing.T) {
			if got := Quote(tt.in); got != tt.want {
				t.Fatalf("Quote(%q) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

// TestQuoteRoundTripsThroughAShellLexer checks the decoding side by hand: the
// result must be a sequence of single-quoted runs (plus the escaped-quote
// sequences between them) that the shell reads back as exactly the input. decodeWord
// fails on any byte a shell would treat as syntax, so a Quote that stopped
// quoting part of its input cannot pass by reusing Quote's own replace.
func TestQuoteRoundTripsThroughAShellLexer(t *testing.T) {
	for _, tt := range tableCases() {
		t.Run(tt.name, func(t *testing.T) {
			quoted := Quote(tt.in)
			got, err := decodeWord(quoted)
			if err != nil {
				t.Fatalf("Quote(%q) = %s is not a single-quoted word: %v", tt.in, quoted, err)
			}
			if got != tt.in {
				t.Fatalf("a shell reads Quote(%q) = %s back as %q", tt.in, quoted, got)
			}
		})
	}
}

// FuzzQuoteRoundTripsThroughAShellLexer generalises the property to arbitrary
// bytes: whatever the input, the result is one shell word that decodes to it.
// A mutation that quotes only "dangerous" bytes, or that forgets the escaped
// quote form, fails here with the offending input in the failure message.
func FuzzQuoteRoundTripsThroughAShellLexer(f *testing.F) {
	for _, tt := range tableCases() {
		f.Add(tt.in)
	}
	f.Fuzz(func(t *testing.T, in string) {
		quoted := Quote(in)
		if quoted == "" {
			t.Fatal("Quote returned the empty string: an empty word must quote as ''")
		}
		got, err := decodeWord(quoted)
		if err != nil {
			t.Fatalf("Quote(%q) = %q is not a single-quoted word: %v", in, quoted, err)
		}
		if got != in {
			t.Fatalf("a shell reads Quote(%q) = %q back as %q", in, quoted, got)
		}
	})
}

// decodeWord interprets word the way a POSIX shell would if the whole word were
// built from single-quoted runs — the only form Quote emits. It rejects any
// byte outside those runs (whitespace, an expansion, a redirection) and any
// escape other than the between-runs quote, so it is an independent
// decoder rather than an inverse of Quote's own replacement.
func decodeWord(word string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(word); {
		switch word[i] {
		case '\'':
			end := strings.IndexByte(word[i+1:], '\'')
			if end < 0 {
				return "", fmt.Errorf("unterminated single quote at byte %d of %s", i, word)
			}
			out.WriteString(word[i+1 : i+1+end])
			i += end + 2
		case '\\':
			if i+1 >= len(word) || word[i+1] != '\'' {
				return "", fmt.Errorf("unsupported escape at byte %d of %s", i, word)
			}
			out.WriteByte('\'')
			i += 2
		default:
			return "", fmt.Errorf("byte %q at %d of %s is outside the quotes: a shell would expand it",
				word[i], i, word)
		}
	}
	return out.String(), nil
}
