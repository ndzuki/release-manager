// Package docfacts audits documentation claims that can be re-derived from a
// configuration file. It exists because the documentation gate only proves that a
// cited line number exists, never that the prose around it is true: three times in
// one session the repository shipped prose that contradicted the config it cited
// (docs claimed nginx did not proxy /audit.v1. while nginx.conf:91 proxied it;
// prose said the host port band was 8082-8087 while DEV_PORTS ended at 8088;
// documents enumerated five proxy prefixes when there were seven).
//
// The rules here are deliberately few and narrow. Each one compares a specific,
// machine-derivable fact against a specific shape of claim, so a finding is worth
// reading; anything fuzzier belongs in review, not in a report that would train its
// readers to ignore it. Findings never change an exit code -- this is an audit, not
// a gate.
package docfacts

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Facts are the values the rules check claims against, derived from the files that
// own them (never from the documentation).
type Facts struct {
	// NginxPackagePrefixes lists the proto package prefixes web/nginx.conf
	// proxies, sorted; NginxPrefixLine points each one at its location line.
	NginxPackagePrefixes []string
	NginxPrefixLine      map[string]int
	// VitePrefixes lists the package prefixes the Vite dev proxy forwards.
	VitePrefixes    []string
	VitePrefixLine  map[string]int
	HostPortLow     int
	HostPortHigh    int
	HostPortSource  string
	hasHostPortBand bool
}

// HasHostPortBand reports whether a port band was derived at all, so the port rule
// stays silent instead of inventing a claim when the source file changed shape.
func (f Facts) HasHostPortBand() bool { return f.hasHostPortBand }

// Item is one documentation line plus the context the rules need: PrevText carries
// the previous few non-empty lines, because a historical qualifier is usually written
// a line or two above the claim it covers.
type Item struct {
	Path     string
	Line     int
	Text     string
	PrevText string
}

// Finding is one claim that contradicts its source of truth.
type Finding struct {
	Path    string
	Line    int
	Rule    string
	Message string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: [%s] %s", f.Path, f.Line, f.Rule, f.Message)
}

// historicalMarkers qualify a claim as a record of what used to be true. The rules
// skip such lines (and lines whose predecessor carries a marker), because
// docs/ux-review.md and docs/user-manual.md deliberately keep the pre-fix B5
// evidence next to its correction.
var historicalMarkers = []string{
	"修复前", "曾经", "历史", "此前", "之前", "pre-fix", "before the fix",
}

var (
	nginxPrefixRe = regexp.MustCompile(`(?m)^\s*location\s+\^~\s+(/[A-Za-z0-9_.]+\.v1\.)\s*\{`)
	vitePrefixRe  = regexp.MustCompile(`(?m)^\s*'(/[A-Za-z0-9_.]+\.v1\.)':\s*\{`)
	devPortsRe    = regexp.MustCompile(`DEV_PORTS=\(([^)]*)\)`)
	portRangeRe   = regexp.MustCompile(`(\d{4})\s*[-–]\s*(\d{4})`)
	prefixInText  = regexp.MustCompile(`/[A-Za-z0-9_]+\.v1\.`)
	// The negation and the routing word both have to sit next to the prefix. A whole
	// line is too wide: SECURITY.md's table row says the entry proxy forwards seven
	// prefixes AND that the notifier/operator ones have no JWT+Casbin behind them,
	// and only the second half is about authentication.
	negationWords = []string{"没有", "未被", "未代理", "未反代", "not proxied", "not reverse", "no location"}
	routingWords  = []string{"location", "代理", "proxied", "反代", "route"}
	// 20 runes covers the usual shapes -- "`/x.v1.` 未被 nginx 代理", "没有 location"
	// right after the prefix -- while staying short of a neighbouring clause: in
	// SECURITY.md's table row the "no JWT/Casbin" half sits ~24 runes past the
	// prefix, so the routing word in the other column cannot pair with it.
	contextWindow = 20
	exclusiveWord = []string{"只反代", "只代理", "只列", "仅反代", "仅代理", "only proxies", "only proxied"}
)

// ParseNginxPrefixes derives the proxied proto package prefixes from web/nginx.conf.
func ParseNginxPrefixes(content string) (prefixes []string, at map[string]int) {
	lines := strings.Split(content, "\n")
	at = map[string]int{}
	for i, line := range lines {
		if m := nginxPrefixRe.FindStringSubmatch(line); m != nil {
			if _, seen := at[m[1]]; !seen {
				at[m[1]] = i + 1
			}
		}
	}
	return sortedKeys(at), at
}

// ParseVitePrefixes derives the dev-proxy prefixes from web/vite.config.ts.
func ParseVitePrefixes(content string) (prefixes []string, at map[string]int) {
	lines := strings.Split(content, "\n")
	at = map[string]int{}
	for i, line := range lines {
		if m := vitePrefixRe.FindStringSubmatch(line); m != nil {
			if _, seen := at[m[1]]; !seen {
				at[m[1]] = i + 1
			}
		}
	}
	return sortedKeys(at), at
}

// ParseDevPorts derives the host port band from deploy/dev/lib/host.sh's DEV_PORTS.
func ParseDevPorts(content string) (low, high int, ok bool) {
	m := devPortsRe.FindStringSubmatch(content)
	if m == nil {
		return 0, 0, false
	}
	ports := []int{}
	for _, field := range strings.Fields(m[1]) {
		if n, err := strconv.Atoi(strings.TrimSpace(field)); err == nil {
			ports = append(ports, n)
		}
	}
	if len(ports) == 0 {
		return 0, 0, false
	}
	low, high = ports[0], ports[0]
	for _, p := range ports {
		if p < low {
			low = p
		}
		if p > high {
			high = p
		}
	}
	return low, high, true
}

// NewFacts assembles the facts from the three owning files' contents.
func NewFacts(nginxConf, viteConfig, hostSh, hostShPath string) Facts {
	nginxPrefixes, nginxAt := ParseNginxPrefixes(nginxConf)
	vitePrefixes, viteAt := ParseVitePrefixes(viteConfig)
	low, high, ok := ParseDevPorts(hostSh)
	return Facts{
		NginxPackagePrefixes: nginxPrefixes,
		NginxPrefixLine:      nginxAt,
		VitePrefixes:         vitePrefixes,
		VitePrefixLine:       viteAt,
		HostPortLow:          low,
		HostPortHigh:         high,
		HostPortSource:       hostShPath,
		hasHostPortBand:      ok,
	}
}

// Check runs every rule over the documentation items.
func Check(items []Item, facts Facts) []Finding {
	findings := []Finding{}
	for _, item := range items {
		if isHistorical(item) {
			continue
		}
		findings = append(findings, checkAbsentPrefix(item, facts)...)
		findings = append(findings, checkPortBand(item, facts)...)
		findings = append(findings, checkExclusiveEnumeration(item, facts)...)
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Path != findings[j].Path {
			return findings[i].Path < findings[j].Path
		}
		return findings[i].Line < findings[j].Line
	})
	return findings
}

// checkAbsentPrefix flags prose that says a prefix nginx.conf proxies is missing.
func checkAbsentPrefix(item Item, facts Facts) []Finding {
	out := []Finding{}
	for _, prefix := range facts.NginxPackagePrefixes {
		if !claimsMissingNear(item.Text, prefix) {
			continue
		}
		line := facts.NginxPrefixLine[prefix]
		out = append(out, Finding{
			Path: item.Path, Line: item.Line, Rule: "absent-prefix",
			Message: fmt.Sprintf("the text says %s is not proxied, but web/nginx.conf:%d has `location ^~ %s`",
				prefix, line, prefix),
		})
	}
	return out
}

// checkPortBand flags a stated host port range that is not the DEV_PORTS band.
func checkPortBand(item Item, facts Facts) []Finding {
	if !facts.HasHostPortBand() || !strings.Contains(item.Text, "端口段") {
		return nil
	}
	out := []Finding{}
	for _, m := range portRangeRe.FindAllStringSubmatch(item.Text, -1) {
		low, lowErr := strconv.Atoi(m[1])
		high, highErr := strconv.Atoi(m[2])
		if lowErr != nil || highErr != nil {
			continue
		}
		if low == facts.HostPortLow && high == facts.HostPortHigh {
			continue
		}
		out = append(out, Finding{
			Path: item.Path, Line: item.Line, Rule: "port-band",
			Message: fmt.Sprintf("the text states the host port band %d-%d, but %s sets DEV_PORTS to %d-%d",
				low, high, facts.HostPortSource, facts.HostPortLow, facts.HostPortHigh),
		})
	}
	return out
}

// checkExclusiveEnumeration flags an exclusivity claim ("proxies only X, Y") whose
// list is a strict subset of what the proxy config actually forwards.
func checkExclusiveEnumeration(item Item, facts Facts) []Finding {
	if !containsAny(item.Text, exclusiveWord) {
		return nil
	}
	out := []Finding{}
	type subject struct {
		mentions string
		prefixes []string
		at       map[string]int
		source   string
	}
	subjects := []subject{
		{"nginx.conf", facts.NginxPackagePrefixes, facts.NginxPrefixLine, "web/nginx.conf"},
		{"vite.config.ts", facts.VitePrefixes, facts.VitePrefixLine, "web/vite.config.ts"},
	}
	for _, s := range subjects {
		if !strings.Contains(item.Text, s.mentions) || len(s.prefixes) == 0 {
			continue
		}
		listed := map[string]bool{}
		for _, prefix := range prefixInText.FindAllString(item.Text, -1) {
			listed[prefix] = true
		}
		if len(listed) == 0 {
			continue
		}
		missing := []string{}
		for _, prefix := range s.prefixes {
			if !listed[prefix] {
				missing = append(missing, fmt.Sprintf("%s (:%d)", prefix, s.at[prefix]))
			}
		}
		if len(missing) == 0 {
			continue
		}
		out = append(out, Finding{
			Path: item.Path, Line: item.Line, Rule: "exclusive-enumeration",
			Message: fmt.Sprintf("the text says %s forwards only the listed prefixes (%d), but it also forwards %s",
				s.source, len(listed), strings.Join(missing, ", ")),
		})
	}
	return out
}

func isHistorical(item Item) bool {
	if containsAny(item.Text, historicalMarkers) {
		return true
	}
	// A qualifier can sit a couple of lines above its claim: the B5 write-ups open a
	// block with "（修复前）" and then spend two or three lines describing it, and
	// docs/testing.md heads a paragraph "TASK-249 之前的坑".
	if item.PrevText != "" && containsAny(item.PrevText, historicalMarkers) {
		return true
	}
	// Strikethrough marks a superseded claim without a prose qualifier.
	return strings.Contains(item.Text, "~~")
}

// claimsMissingNear reports whether text negates routing FOR THAT PREFIX: a
// negation and a routing word must both appear within contextWindow bytes of one of
// the prefix's mentions.
func claimsMissingNear(text, prefix string) bool {
	haystack := []rune(text)
	needle := []rune(prefix)
	for start := 0; start+len(needle) <= len(haystack); start++ {
		if string(haystack[start:start+len(needle)]) != prefix {
			continue
		}
		lo := start - contextWindow
		if lo < 0 {
			lo = 0
		}
		hi := start + len(needle) + contextWindow
		if hi > len(haystack) {
			hi = len(haystack)
		}
		window := string(haystack[lo:hi])
		if containsAny(window, negationWords) && containsAny(window, routingWords) {
			return true
		}
	}
	return false
}

func containsAny(text string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
