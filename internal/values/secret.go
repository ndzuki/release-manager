package values

import "regexp"

// secretPatterns are regexps matching common secret key names followed by a value.
// Post-filtering removes false positives like empty strings, null, and template references.
var secretPatterns = []*regexp.Regexp{
	// password / secret / token / api_key / private_key / access_key patterns
	regexp.MustCompile(`(?im)(?:^|\s)(?:["']?(?:password|secret|token|api[_-]?key|apikey|private[_-]?key|access[_-]?key)["']?\s*[:=]\s*)([^\s#].+)`),
	// AWS-style key ID pattern: AKIA... (20 chars)
	regexp.MustCompile(`\bAKIA[A-Z0-9]{16}\b`),
}

// refPatterns match values that look like secret references, not literals.
var refPatterns = regexp.MustCompile(`^(\$\{|ref\s|\{\{|<ref>|\[ref\]|null$|""$|''$)`)

// ContainsSecret returns true if input contains patterns that look like
// literal secrets (passwords, API keys, tokens).
func ContainsSecret(input []byte) bool {
	s := string(input)
	for _, pat := range secretPatterns {
		matches := pat.FindAllStringSubmatch(s, -1)
		for _, m := range matches {
			// m[0] is the full match, m[1] is the captured value (if any)
			if len(m) >= 2 {
				val := stripQuotes(m[1])
				if val == "" || refPatterns.MatchString(val) {
					continue
				}
				return true
			} else {
				// No capture group (e.g. AKIA pattern)
				return true
			}
		}
	}
	return false
}

// stripQuotes removes surrounding double/single quotes from a value.
func stripQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
