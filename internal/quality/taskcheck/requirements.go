package taskcheck

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Finding kinds for the REQ side of the delivery ledger.
const (
	// KindDeliveryRecordNotDelivered: a REQ carries a 交付记录 but its status
	// is not delivered.
	KindDeliveryRecordNotDelivered = "delivery_record_not_delivered"
	// KindDeliveryRecordNotVerified: a REQ carries a 交付记录 but is not
	// verified.
	KindDeliveryRecordNotVerified = "delivery_record_not_verified"
	// KindDeliveredNotVerified: a REQ is delivered but not verified.
	KindDeliveredNotVerified = "delivered_not_verified"
	// KindVerifiedNotDelivered: a REQ is verified but not delivered.
	KindVerifiedNotDelivered = "verified_not_delivered"
)

// Requirement is one REQ card's delivery-ledger state.
type Requirement struct {
	Path              string
	Status            string
	Verified          bool
	HasDeliveryRecord bool
}

// ParseRequirement reads a REQ card: the frontmatter status/verified fields and
// whether the body carries a `## 交付记录` section.
func ParseRequirement(path string) (Requirement, error) {
	f, err := os.Open(path)
	if err != nil {
		return Requirement{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	req := Requirement{Path: path}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	state := reqBeforeFrontmatter
	closed := false
	for scanner.Scan() {
		next, justClosed := scanRequirementLine(&req, scanner.Text(), state)
		state = next
		if justClosed {
			closed = true
		}
	}
	if err := scanner.Err(); err != nil {
		return Requirement{}, fmt.Errorf("read %s: %w", path, err)
	}
	if !closed {
		return Requirement{}, fmt.Errorf("%s: no frontmatter block found", path)
	}
	return req, nil
}

// CheckRequirements enforces the delivery-ledger agreement the project's
// conventions require: a `## 交付记录` section means the REQ is delivered and
// verified, and delivered/verified must never disagree.
//
// Why this exists: the 2026-09 audit found REQ-065 carrying a delivery record
// while its status was still `accepted` and it had no `verified` field -- a
// half-written ledger that reads as "delivered" to a human and as "not
// delivered" to any tally. The rule is mechanical on purpose; the evidence for
// each AC is still a human judgement recorded in the card.
func CheckRequirements(reqs []Requirement) *Result {
	result := &Result{}
	for _, req := range reqs {
		if !req.HasDeliveryRecord && req.Status != "delivered" && !req.Verified {
			continue
		}
		result.Checked++

		if req.HasDeliveryRecord && req.Status != "delivered" {
			result.Findings = append(result.Findings, Finding{
				Path:     req.Path,
				Kind:     KindDeliveryRecordNotDelivered,
				Severity: SeverityViolation,
				Message: fmt.Sprintf("carries a 交付记录 but status=%q, want delivered",
					req.Status),
			})
		}
		if req.HasDeliveryRecord && !req.Verified {
			result.Findings = append(result.Findings, Finding{
				Path:     req.Path,
				Kind:     KindDeliveryRecordNotVerified,
				Severity: SeverityViolation,
				Message:  "carries a 交付记录 but verified is not true",
			})
		}
		if req.Status == "delivered" && !req.Verified {
			result.Findings = append(result.Findings, Finding{
				Path:     req.Path,
				Kind:     KindDeliveredNotVerified,
				Severity: SeverityViolation,
				Message:  "status=delivered requires verified: true",
			})
		}
		if req.Verified && req.Status != "delivered" {
			result.Findings = append(result.Findings, Finding{
				Path:     req.Path,
				Kind:     KindVerifiedNotDelivered,
				Severity: SeverityViolation,
				Message:  fmt.Sprintf("verified: true requires status=delivered, got %q", req.Status),
			})
		}
	}
	return result
}

// reqScanState tracks where the scanner is in a REQ document.
type reqScanState int

const (
	reqBeforeFrontmatter reqScanState = iota
	reqInFrontmatter
	reqInBody
)

// scanRequirementLine folds one line into req and returns the next state plus
// whether this line closed the frontmatter block.
//
// A `---` after the frontmatter has closed is a body horizontal rule or table
// separator and must NOT re-open it, or body text would be parsed as fields
// (the first run of this gate reported garbage statuses that way).
func scanRequirementLine(req *Requirement, line string, state reqScanState) (reqScanState, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "---" {
		switch state {
		case reqBeforeFrontmatter:
			return reqInFrontmatter, false
		case reqInFrontmatter:
			return reqInBody, true
		default:
			return state, false
		}
	}
	switch state {
	case reqInFrontmatter:
		if key, value, ok := strings.Cut(line, ":"); ok {
			req.applyFrontmatter(key, unquote(strings.TrimSpace(value)))
		}
	case reqInBody:
		if strings.HasPrefix(trimmed, "## ") && strings.Contains(trimmed, "交付记录") {
			req.HasDeliveryRecord = true
		}
	}
	return state, false
}

func (r *Requirement) applyFrontmatter(key, value string) {
	switch strings.TrimSpace(key) {
	case "status":
		r.Status = value
	case "verified":
		r.Verified = value == "true"
	}
}
