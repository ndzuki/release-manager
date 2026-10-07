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
	VitePrefixes   []string
	VitePrefixLine map[string]int
	// NginxPackages and VitePackages are the bare package names behind those
	// prefixes (/auth.v1. -> auth), for prose that enumerates packages instead of
	// prefixes ("only proxies auth/orchestrator/webhook/...").
	NginxPackages   []string
	VitePackages    []string
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
	"修复前", "曾经", "历史", "此前", "原来", "pre-fix", "before the fix",
}

// historicalRefRe covers "TASK-249 之前" style qualifiers. A bare "之前" used to be a
// marker too, until review showed it swallows real claims: "在运行 dev-up 之前请检查…"
// on the line above exempted the next line's contradiction.
var historicalRefRe = regexp.MustCompile(`TASK-\d+\s*之前`)

var (
	nginxPrefixRe = regexp.MustCompile(`(?m)^\s*location\s+\^~\s+(/[A-Za-z0-9_.]+\.v1\.)\s*\{`)
	vitePrefixRe  = regexp.MustCompile(`(?m)^\s*'(/[A-Za-z0-9_.]+\.v1\.)':\s*\{`)
	devPortsRe    = regexp.MustCompile(`DEV_PORTS=\(([^)]*)\)`)
	// A band claim is a range that STARTS at the derived low end and is anchored to
	// four-digit endpoints, so a NodePort range beside it (8082-8088:30082-30088) cannot
	// be parsed as 82-3008 and an unrelated four-digit range elsewhere is not mistaken
	// for the dev band.
	portBandRe   = regexp.MustCompile(`(?:^|\D)(\d{4})\s*[-–]\s*(\d{4})(?:\D|$)`)
	prefixInText = regexp.MustCompile(`/[A-Za-z0-9_]+\.v1\.`)
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
	// Context vocabulary for a port statement. This used to demand the literal
	// "端口段", which twelve lines in this repository do not use; the range anchor in
	// checkPortBand is what keeps the rule precise.
	portWords = []string{"端口", "宿主", "host", "port", "管理面", "dev-up"}
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
		NginxPackages:        packageNames(nginxPrefixes),
		VitePackages:         packageNames(vitePrefixes),
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
		findings = append(findings, checkExclusiveEnumerationByName(item, facts)...)
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
	if !facts.HasHostPortBand() || !containsAny(item.Text, portWords) {
		return nil
	}
	out := []Finding{}
	for _, m := range portBandRe.FindAllStringSubmatch(item.Text, -1) {
		low, lowErr := strconv.Atoi(m[1])
		high, highErr := strconv.Atoi(m[2])
		if lowErr != nil || highErr != nil {
			continue
		}
		// The regex captures any four-digit range; only one that starts at the derived
		// low end is a claim about THE band.
		if low != facts.HostPortLow || high == facts.HostPortHigh {
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

// checkExclusiveEnumerationByName covers prose that enumerates PACKAGE names rather
// than /x.v1. prefixes ("only proxies auth/orchestrator/webhook/operator/notifier").
// Review found the prefix-shaped rule missed that form entirely.
func checkExclusiveEnumerationByName(item Item, facts Facts) []Finding {
	if !containsAny(item.Text, exclusiveWord) {
		return nil
	}
	out := []Finding{}
	subjects := []struct {
		packages []string
		at       map[string]int
		source   string
	}{
		{facts.NginxPackages, facts.NginxPrefixLine, "web/nginx.conf"},
		{facts.VitePackages, facts.VitePrefixLine, "web/vite.config.ts"},
	}
	// Report the subject the text is actually talking about: a line that names
	// nginx.conf should not also be reported against vite.config.ts.
	mentionsNginx := strings.Contains(item.Text, "nginx.conf")
	mentionsVite := strings.Contains(item.Text, "vite.config.ts")
	for _, s := range subjects {
		if s.source == "web/nginx.conf" && mentionsVite && !mentionsNginx {
			continue
		}
		if s.source == "web/vite.config.ts" && mentionsNginx && !mentionsVite {
			continue
		}
		listed := enumeratesNames(item.Text, s.packages)
		if len(listed) < 2 {
			// One package name is a statement about that package, not an enumeration
			// of what the proxy forwards.
			continue
		}
		missing := []string{}
		for _, name := range s.packages {
			if _, ok := listed[name]; !ok {
				missing = append(missing, fmt.Sprintf("%s (%s)", name, prefixForName(name, s.at)))
			}
		}
		if len(missing) == 0 {
			continue
		}
		out = append(out, Finding{
			Path: item.Path, Line: item.Line, Rule: "exclusive-enumeration-by-name",
			Message: fmt.Sprintf("the text says %s forwards only the listed packages (%d), but it also forwards %s",
				s.source, len(listed), strings.Join(missing, ", ")),
		})
	}
	return out
}

// enumeratesNames returns the package names the text lists as one enumeration: at
// least two names separated only by separators, so "only proxies auth for this path,
// ask orchestrator otherwise" is not read as a list of the proxied set.
func enumeratesNames(text string, names []string) map[string]struct{} {
	listed := map[string]struct{}{}
	for _, a := range names {
		for _, b := range names {
			if a == b {
				continue
			}
			for _, pa := range wordPositions(text, a) {
				for _, pb := range wordPositions(text, b) {
					// Ordered pair: b follows a with nothing but list punctuation
					// between them. Word boundaries keep "authz" from matching "auth",
					// so an empty gap can only mean the two names really abut.
					if pa < pb && onlySeparators(text[pa+len(a):pb]) {
						listed[a] = struct{}{}
						listed[b] = struct{}{}
					}
				}
			}
		}
	}
	return listed
}

// onlySeparators reports whether a gap between two package names is nothing but list
// punctuation, so an enumeration is recognised and a sentence is not.
func onlySeparators(gap string) bool {
	if strings.TrimSpace(gap) == "and" {
		return true
	}
	for _, r := range gap {
		switch r {
		case ' ', '\t', '/', '、', ',', '，', '和':
		default:
			return false
		}
	}
	return true
}

// wordPositions finds name at word boundaries so "auth" does not match "authz".
func wordPositions(text, name string) []int {
	out := []int{}
	for start := 0; ; {
		idx := strings.Index(text[start:], name)
		if idx < 0 {
			return out
		}
		pos := start + idx
		if boundaryAt(text, pos-1) && boundaryAt(text, pos+len(name)) {
			out = append(out, pos)
		}
		start = pos + len(name)
	}
}

func boundaryAt(text string, i int) bool {
	if i < 0 || i >= len(text) {
		return true
	}
	c := text[i]
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_':
		return false
	}
	return true
}

func prefixForName(name string, at map[string]int) string {
	for prefix := range at {
		if packageName(prefix) == name {
			return prefix
		}
	}
	return name
}

func packageName(prefix string) string {
	return strings.TrimSuffix(strings.TrimPrefix(prefix, "/"), ".v1.")
}

func packageNames(prefixes []string) []string {
	out := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		out = append(out, packageName(prefix))
	}
	return out
}

func isHistorical(item Item) bool {
	if isHistoricalText(item.Text) {
		return true
	}
	// A qualifier can sit a couple of lines above its claim: the B5 write-ups open a
	// block with "（修复前）" and then spend two or three lines describing it, and
	// docs/testing.md heads a paragraph "TASK-249 之前的坑".
	if item.PrevText != "" && isHistoricalText(item.PrevText) {
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

// isHistoricalText reports whether a line qualifies a claim as history.
func isHistoricalText(text string) bool {
	if containsAny(text, historicalMarkers) {
		return true
	}
	return historicalRefRe.MatchString(text)
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
