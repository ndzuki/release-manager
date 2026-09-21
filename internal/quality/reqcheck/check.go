// Package reqcheck validates atomic requirement documents against the project's
// REQ template.
//
// The gate used to require REQ-039's full 10-section template from every
// document, with no frontmatter awareness. That contradicted the project's own
// convention (AGENTS.md: the default `lite` tier needs 要做什么 / 验收标准 /
// 非目标; the `full` tier adds the interface, data, security and rollback
// sections), so the gate could never pass on the real vault: 82/82 REQs failed
// with 203 missing-section findings. It was invisible because the Makefile
// target skipped silently when REQS_DIR was unset (TASK-159).
//
// The gate is now scope- and tier-aware:
//
//   - a document with `delivery_scope: index` or `archived` is skipped (it is a
//     roadmap/domain index or an archived record, not an implementable REQ);
//   - every other document must carry the lite core sections, accepting the
//     vocabulary the vault actually uses (目标/要做什么, 验收标准/完成标准,
//     非目标/范围与边界);
//   - the seven full-only sections are required only when the document declares
//     `tier: full`.
//
// A section may still be present as "不适用" with a reason.
package reqcheck

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// RequiredSections enumerates the full 10-section template (REQ-039). It is the
// union of LiteSections and FullOnlySections, kept for tests that build a
// complete document; validation uses the tier-aware lists below.
var RequiredSections = []string{
	"目标",
	"影响服务",
	"输入契约",
	"输出契约",
	"状态与数据",
	"错误模型",
	"安全边界",
	"验收标准",
	"非目标",
	"回滚方式",
}

// LiteSections are required of every non-skipped REQ. Each entry lists the
// accepted headings for one logical section, so the gate matches the vocabulary
// the vault actually uses instead of one spelling.
var LiteSections = [][]string{
	{"目标", "要做什么"},
	{"验收标准", "完成标准"},
	{"非目标", "范围与边界"},
}

// FullOnlySections are required only when the document declares `tier: full`
// (AGENTS.md: a REQ that touches interfaces, data, security or anything
// irreversible is promoted to full).
var FullOnlySections = []string{
	"影响服务",
	"输入契约",
	"输出契约",
	"状态与数据",
	"错误模型",
	"安全边界",
	"回滚方式",
}

// Violation describes a single validation failure.
type Violation struct {
	File    string
	Line    int
	CheckID string
	Message string
}

func (v Violation) String() string {
	if v.Line > 0 {
		return fmt.Sprintf("%s:%d: [%s] %s", v.File, v.Line, v.CheckID, v.Message)
	}
	return fmt.Sprintf("%s: [%s] %s", v.File, v.CheckID, v.Message)
}

// Result holds the outcome of validating a single file.
type Result struct {
	File       string
	Violations []Violation
	Sections   map[string]bool // section name → present
	NA         map[string]bool // section name → explicitly marked N/A
	// DeliveryScope is the frontmatter delivery_scope (index/archived/empty).
	DeliveryScope string
	// Tier is the frontmatter tier (full/empty for the lite default).
	Tier string
	// Skipped reports that the document is out of scope for this gate
	// (delivery_scope index/archived) and was not validated.
	Skipped bool
}

var (
	sectionRe = regexp.MustCompile(`^##\s+(.+)$`)
	naRe      = regexp.MustCompile(`^不适用(?:$|[ \t：:，,。；;—-]+(.*)$)`)
	// The vault writes acceptance ids in bold (`- [x] **AC-077-01** Given ...`),
	// so the markers are optional: requiring the bare form flagged 117 findings
	// that were all formatting, not missing criteria (TASK-159).
	acceptanceRe       = regexp.MustCompile(`^-\s*\[[ xX]\]\s+\*{0,2}AC-\d{3}-\d{2}\*{0,2}(?:\s|:|$)`)
	acceptanceMarkerRe = regexp.MustCompile(`(?i)AC-[A-Z0-9-]+`)
	checkboxRe         = regexp.MustCompile(`^(?:[-*+]\s*)?\[[ xX]\]`)
	givenWhenThenRe    = regexp.MustCompile(`(?is)\bgiven\b.*\bwhen\b.*\bthen\b`)
)

// Check validates a single requirement document.
func Check(path string) (*Result, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	frontmatter, body := splitFrontmatter(string(raw))

	result := &Result{
		File:          path,
		Sections:      make(map[string]bool),
		NA:            make(map[string]bool),
		DeliveryScope: frontmatterValue(frontmatter, "delivery_scope"),
		Tier:          frontmatterValue(frontmatter, "tier"),
	}
	// Roadmap/domain indices and archived records are not implementable REQs:
	// they have no acceptance criteria by design and must not fail a structure
	// gate (ADR-000 defines REQ-001..008 as indices; delivery_scope records it).
	if result.DeliveryScope == "index" || result.DeliveryScope == "archived" {
		result.Skipped = true
		return result, nil
	}

	scanner := bufio.NewScanner(strings.NewReader(body))
	currentSec := ""
	acceptanceACs := 0
	acceptanceItems := 0
	for lineNum := 1; scanner.Scan(); lineNum++ {
		trimmed := strings.TrimSpace(scanner.Text())

		if matches := sectionRe.FindStringSubmatch(trimmed); matches != nil {
			currentSec = strings.TrimSpace(matches[1])
			result.Sections[currentSec] = true
			continue
		}

		if currentSec == "" {
			continue
		}

		validateNA(result, currentSec, trimmed, lineNum)

		if currentSec == "验收标准" {
			if validateAcceptance(result, trimmed, lineNum) {
				acceptanceACs++
			}
			if isAcceptanceCandidate(trimmed) {
				acceptanceItems++
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	validateRequiredSections(result)
	validateAcceptanceSection(result, acceptanceACs, acceptanceItems)

	return result, nil
}

// splitFrontmatter returns the YAML frontmatter block and the body. A document
// without frontmatter yields an empty block and the whole text as body.
func splitFrontmatter(text string) (frontmatter, body string) {
	if !strings.HasPrefix(text, "---") {
		return "", text
	}
	rest := text[3:]
	if i := strings.Index(rest, "\n---"); i >= 0 {
		return rest[:i], rest[i+4:]
	}
	return "", text
}

// frontmatterValue reads a scalar frontmatter field.
func frontmatterValue(frontmatter, key string) string {
	for _, line := range strings.Split(frontmatter, "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) != key {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return ""
}

// anyPresent reports whether any of the accepted headings was seen.
func anyPresent(result *Result, headings []string) bool {
	for _, heading := range headings {
		if hasSection(result, heading) {
			return true
		}
	}
	return false
}

// hasSection reports whether a heading for name is present. A heading may carry
// a clarifying suffix -- REQ-086 writes "验收标准（D1~D7 = A 细化）" -- so the
// match is by prefix, not equality.
func hasSection(result *Result, name string) bool {
	if result.Sections[name] {
		return true
	}
	for heading := range result.Sections {
		if strings.HasPrefix(heading, name) {
			return true
		}
	}
	return false
}

func validateNA(result *Result, section, line string, lineNum int) {
	matches := naRe.FindStringSubmatch(line)
	if matches == nil {
		return
	}

	result.NA[section] = true
	reason := ""
	if len(matches) > 1 {
		reason = strings.Trim(matches[1], " \t：:—-。.;；")
	}
	if reason != "" {
		return
	}

	result.Violations = append(result.Violations, Violation{
		File:    result.File,
		Line:    lineNum,
		CheckID: "CHK-02",
		Message: fmt.Sprintf("section %q is marked 不适用 without a reason", section),
	})
}

func validateAcceptance(result *Result, line string, lineNum int) bool {
	if !acceptanceRe.MatchString(line) {
		if isAcceptanceCandidate(line) {
			result.Violations = append(result.Violations, Violation{
				File:    result.File,
				Line:    lineNum,
				CheckID: "CHK-03",
				Message: fmt.Sprintf("acceptance criterion must be an AC-XXX-NN checklist item: %q", line),
			})
		}
		return false
	}

	if !containsGivenWhenThen(line) {
		result.Violations = append(result.Violations, Violation{
			File:    result.File,
			Line:    lineNum,
			CheckID: "CHK-03",
			Message: fmt.Sprintf("acceptance criterion lacks Given/When/Then: %q", line),
		})
	}
	if dependsOnManualInterpretation(line) {
		result.Violations = append(result.Violations, Violation{
			File:    result.File,
			Line:    lineNum,
			CheckID: "CHK-04",
			Message: fmt.Sprintf("acceptance criterion depends on manual interpretation: %q", line),
		})
	}

	return true
}

func isAcceptanceCandidate(line string) bool {
	// A blockquote is a note ABOUT the criteria, not a criterion: REQ-010's
	// "采纳建议 auto" note merely names AC-039-01 and was reported as a
	// malformed acceptance item (TASK-159).
	if strings.HasPrefix(strings.TrimSpace(line), ">") {
		return false
	}
	return acceptanceMarkerRe.MatchString(line) || checkboxRe.MatchString(line)
}

func validateRequiredSections(result *Result) {
	for _, headings := range LiteSections {
		if anyPresent(result, headings) {
			continue
		}
		result.Violations = append(result.Violations, missingSection(result, strings.Join(headings, " or ")))
	}
	if result.Tier != "full" {
		return
	}
	for _, section := range FullOnlySections {
		if hasSection(result, section) {
			continue
		}
		result.Violations = append(result.Violations, missingSection(result, section))
	}
}

func missingSection(result *Result, section string) Violation {
	return Violation{
		File:    result.File,
		CheckID: "CHK-01",
		Message: fmt.Sprintf(
			"missing section: %q — add the heading and content, or mark it 不适用 with reason",
			section,
		),
	}
}

func validateAcceptanceSection(result *Result, acceptanceACs, acceptanceItems int) {
	if !result.Sections["验收标准"] || result.NA["验收标准"] || acceptanceACs > 0 || acceptanceItems > 0 {
		return
	}

	result.Violations = append(result.Violations, Violation{
		File:    result.File,
		CheckID: "CHK-03",
		Message: "验收标准 section has no AC-XXX-NN formatted criteria",
	})
}

func containsGivenWhenThen(s string) bool {
	return givenWhenThenRe.MatchString(s)
}

func dependsOnManualInterpretation(s string) bool {
	manualTerms := []string{
		"人工检查", "人工确认", "人工解释", "人工审核", "人工审阅", "人工验证", "人工判断",
		"手动检查", "手动确认", "手动解释", "手动审核", "手动审阅", "手动验证", "手动判断",
		"检查内部实现", "确认内部实现", "解释内部实现", "审核内部实现", "审阅内部实现", "验证内部实现", "判断内部实现",
		"manual inspection", "manual check", "manual confirmation", "manual explanation", "manual verification", "manual review", "manual judgment",
		"inspect internal implementation", "check internal implementation", "verify internal implementation", "review internal implementation", "explain internal implementation",
	}
	lower := strings.ToLower(s)
	for _, term := range manualTerms {
		if !strings.Contains(lower, term) || isManualInterpretationExempt(lower, term) {
			continue
		}
		return true
	}

	return false
}

func isManualInterpretationExempt(s, term string) bool {
	termIndex := strings.Index(s, term)
	if termIndex < 0 {
		return false
	}

	if strings.Contains(s, "不依赖"+term) || strings.Contains(s, "不依赖 "+term) {
		return true
	}

	contextStart := max(0, termIndex-24)
	context := strings.TrimSpace(s[contextStart:termIndex])
	for _, prefix := range []string{
		"不依赖",
		"无需",
		"不需要",
		"without",
		"does not require",
	} {
		if strings.HasSuffix(context, prefix) || strings.HasSuffix(context, prefix+"人工") || strings.HasSuffix(context, prefix+"手动") {
			return true
		}
	}

	return false
}
