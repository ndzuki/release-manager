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
	var (
		lineNum       int
		currentSec    string
		sectionLine   int
		acceptanceACs int
	)
	sectionRe := regexp.MustCompile(`^##\s+(.+)$`)
	naRe := regexp.MustCompile(`^不适用[：:—\-]?\s*(.*)`)

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if m := sectionRe.FindStringSubmatch(trimmed); m != nil {
			title := strings.TrimSpace(m[1])
			result.Sections[title] = true
			currentSec = title
			sectionLine = lineNum
			continue
		}

		// Check for "不适用" marker under current section.
		if currentSec != "" && naRe.MatchString(trimmed) {
			result.NA[currentSec] = true
		}

		// CHK-03: validate acceptance criteria format
		if currentSec == "验收标准" {
			if acRe.MatchString(trimmed) {
				acceptanceACs++
				if !containsGivenWhenThen(trimmed) {
					result.Violations = append(result.Violations, Violation{
						File:    path,
						Line:    lineNum,
						CheckID: "CHK-03",
						Message: fmt.Sprintf("acceptance criterion lacks Given/When/Then: %q", trimmed),
					})
				}
			}
		}
		_ = sectionLine
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	// CHK-01: all 10 sections must exist.
	for _, sec := range RequiredSections {
		if !result.Sections[sec] {
			if result.NA[sec] {
				// Section is present elsewhere or marked N/A — no violation.
			} else {
				result.Violations = append(result.Violations, Violation{
					File:    path,
					CheckID: "CHK-01",
					Message: fmt.Sprintf("missing section: %q — add it or mark as 不适用 with reason", sec),
				})
			}
		}
	}

	// CHK-03: acceptance criteria have AC-XXX-NN format.
	if result.Sections["验收标准"] && acceptanceACs == 0 {
		result.Violations = append(result.Violations, Violation{
			File:    path,
			CheckID: "CHK-03",
			Message: "验收标准 section has no AC-XXX-NN formatted criteria",
		})
	}

	// CHK-02: sections present but empty/only N/A — warn if heading exists
	// but the only content on the next non-blank line is "不适用".
	// This is informational; the section IS present (has heading).

	return result, nil
}

// acRe matches AC-XXX-NN format.
var acRe = regexp.MustCompile(`AC-\d{3}-\d{2}`)

func containsGivenWhenThen(s string) bool {
	lower := strings.ToLower(s)
	return strings.Contains(lower, "given") &&
		strings.Contains(lower, "when") &&
		strings.Contains(lower, "then")
}
