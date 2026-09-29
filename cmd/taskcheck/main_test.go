package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ndzuki/release-manager/internal/quality/taskcheck"
)

// TASK-228: the summary used to add the requirement count to the card count, so a run with
// REQS_DIR reported 265 "completed card(s)" when 203 cards existed. The two populations are
// now reported separately.
func TestSummaryLineKeepsCardsAndRequirementsApart(t *testing.T) {
	cardsOnly := summaryLine(taskcheck.Result{Checked: 203, Verified: 203}, 0, 0)
	assert.Equal(t, "taskcheck: 203 completed card(s) checked, 203 verified, 0 violation(s), 0 unverified", cardsOnly)

	withRequirements := summaryLine(taskcheck.Result{Checked: 203, RequirementsChecked: 62, Verified: 203}, 1, 0)
	assert.Contains(t, withRequirements, "203 completed card(s) and 62 requirement record(s) checked")
	assert.NotContains(t, withRequirements, "265 completed card(s)")
}
