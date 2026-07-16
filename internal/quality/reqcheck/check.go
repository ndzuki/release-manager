// Package reqcheck validates atomic requirement documents against the
// 10-section template defined in REQ-039.
//
// A valid requirement document must have all 10 sections present, or
// explicitly marked "不适用" (N/A) with a reason.
package reqcheck

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// RequiredSections enumerates the 10 mandatory sections.
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
}

var (
	sectionRe    = regexp.MustCompile(`^##\s+(.+)$`)
	naRe         = regexp.MustCompile(`^不适用\s*(?:[：:—-]\s*)?(.*)$`)
	acceptanceRe = regexp.MustCompile(`^-\s*\[[ xX]\]\s+AC-\d{3}-\d{2}(?:\s|$)`)
)

// Check validates a single requirement document.
func Check(path string) (*Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	result := &Result{
		File:     path,
		Sections: make(map[string]bool),
		NA:       make(map[string]bool),
	}

	scanner := bufio.NewScanner(f)
	currentSec := ""
	acceptanceACs := 0

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

		if currentSec == "验收标准" && validateAcceptance(result, trimmed, lineNum) {
			acceptanceACs++
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	validateRequiredSections(result)
	validateAcceptanceSection(result, acceptanceACs)

	return result, nil
}

func validateNA(result *Result, section, line string, lineNum int) {
	matches := naRe.FindStringSubmatch(line)
	if matches == nil {
		return
	}

	result.NA[section] = true
	if strings.TrimSpace(matches[1]) != "" {
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

func validateRequiredSections(result *Result) {
	for _, section := range RequiredSections {
		if result.Sections[section] {
			continue
		}

		result.Violations = append(result.Violations, Violation{
			File:    result.File,
			CheckID: "CHK-01",
			Message: fmt.Sprintf(
				"missing section: %q — add the heading and content, or mark it 不适用 with reason",
				section,
			),
		})
	}
}

func validateAcceptanceSection(result *Result, acceptanceACs int) {
	if !result.Sections["验收标准"] || acceptanceACs > 0 {
		return
	}

	result.Violations = append(result.Violations, Violation{
		File:    result.File,
		CheckID: "CHK-03",
		Message: "验收标准 section has no AC-XXX-NN formatted criteria",
	})
}

func containsGivenWhenThen(s string) bool {
	lower := strings.ToLower(s)
	return strings.Contains(lower, "given") &&
		strings.Contains(lower, "when") &&
		strings.Contains(lower, "then")
}

func dependsOnManualInterpretation(s string) bool {
	manualTerms := []string{
		"人工检查",
		"人工确认",
		"人工解释",
		"手动检查",
		"手动确认",
		"manual inspection",
		"manual verification",
		"manual review",
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
	for _, prefix := range []string{
		"不依赖",
		"无需",
		"不需要",
		"without",
		"does not require",
	} {
		if strings.Contains(s, prefix+term) || strings.Contains(s, prefix+" "+term) {
			return true
		}
	}

	return false
}
