package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ndzuki/release-manager/internal/quality/taskcheck"
)

// TASK-228: the summary used to add the requirement count to the card count, so a run with
// REQS_DIR reported 203 cards + 63 requirement records as 266 "completed card(s)". The two
// populations are now reported separately.
func TestSummaryLineKeepsCardsAndRequirementsApart(t *testing.T) {
	cardsOnly := summaryLine(taskcheck.Result{Checked: 203, Verified: 203}, 0, 0)
	assert.Equal(t, "taskcheck: 203 completed card(s) checked, 203 verified, 0 violation(s), 0 unverified", cardsOnly)

	withRequirements := summaryLine(taskcheck.Result{Checked: 203, RequirementsChecked: 63, Verified: 203}, 0, 0)
	assert.Equal(t,
		"taskcheck: 203 completed card(s) and 63 requirement record(s) checked, 203 verified, 0 violation(s), 0 unverified",
		withRequirements)
	assert.NotContains(t, withRequirements, "266 completed card(s)")
}

// The defect lived in the accumulation, not in the rendering: folding the requirement count into
// Checked here is what a reviewer re-introduced to show the old tests stayed green. This test
// fails if that happens again.
func TestMergeResultsKeepsPopulationsApart(t *testing.T) {
	cards := taskcheck.Result{Checked: 203, Verified: 203}
	requirements := &taskcheck.Result{Checked: 63, Findings: []taskcheck.Finding{{Path: "REQ-1.md"}}}

	mergeResults(&cards, requirements)

	assert.Equal(t, 203, cards.Checked, "the card count must never absorb requirement records")
	assert.Equal(t, 63, cards.RequirementsChecked)
	assert.Equal(t, 203, cards.Verified, "card verification stays untouched by the requirement side")
	assert.Len(t, cards.Findings, 1, "requirement findings still reach the report")
}
