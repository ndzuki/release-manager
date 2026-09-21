package taskcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "TASK-900-sample.md")
	require.NoError(t, os.WriteFile(path, []byte(`---
id: "900"
title: "sample"
status: "closed"
merge_status: unmerged
pr_url: ""
---

# body
`), 0o600))

	card, err := ParseCard(path)
	require.NoError(t, err)
	assert.Equal(t, "closed", card.Status)
	assert.Equal(t, "unmerged", card.MergeStatus)
	assert.Equal(t, "", card.PRURL)
	assert.Equal(t, path, card.Path)
}

func TestParseCardRejectsMissingFrontmatter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "TASK-901.md")
	require.NoError(t, os.WriteFile(path, []byte("# no frontmatter\n"), 0o600))
	_, err := ParseCard(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "frontmatter")
}

func TestParseGitLog(t *testing.T) {
	ev := NewEvidence()
	require.NoError(t, ev.ParseGitLog(strings.NewReader(
		"aaaa\tMerge pull request #12 from ndzuki/task/x\n"+
			"bbbb\tfeat: something\n"+
			"cccc\tMerge pull request #13 from ndzuki/task/y\n")))

	assert.True(t, ev.HasCommit("aaaa"))
	assert.True(t, ev.HasCommit("bbbb"))
	assert.Equal(t, "aaaa", ev.prFromSubject[12])
	assert.Equal(t, "cccc", ev.prFromSubject[13])

	// The "(#N)" squash shape is not treated as a PR number: this repository
	// also writes task numbers in that position ("(#017)").
	ev2 := NewEvidence()
	require.NoError(t, ev2.ParseGitLog(strings.NewReader("dddd\tmerge: inventory (#017)\n")))
	assert.Empty(t, ev2.prFromSubject)
}

func TestParseGHPullList(t *testing.T) {
	ev := NewEvidence()
	require.NoError(t, ev.ParseGHPullList(strings.NewReader(
		`[{"number":5,"state":"MERGED","mergeCommit":{"oid":"abc"}},`+
			`{"number":6,"state":"OPEN","mergeCommit":null}]`)))
	require.Contains(t, ev.prs, 5)
	assert.Equal(t, "MERGED", ev.prs[5].State)
	require.NotNil(t, ev.prs[5].MergeCommit)
	assert.Equal(t, "abc", ev.prs[5].MergeCommit.OID)
	require.Contains(t, ev.prs, 6)
	assert.Nil(t, ev.prs[6].MergeCommit)
}

func TestParseGHPullListEmptyIsNotAnError(t *testing.T) {
	ev := NewEvidence()
	require.NoError(t, ev.ParseGHPullList(strings.NewReader("")))
	require.NoError(t, ev.ParseGHPullList(strings.NewReader("  \n")))
	assert.Empty(t, ev.prs)
	require.Error(t, ev.ParseGHPullList(strings.NewReader("{not json")))
}

func TestGithubPRNumber(t *testing.T) {
	tests := []struct {
		url  string
		want int
		ok   bool
	}{
		{"https://github.com/ndzuki/release-manager/pull/101", 101, true},
		{"https://github.com/ndzuki/release-manager/pull/7/files", 7, true},
		{"http://github.com/o/r/pull/3", 3, true},
		{"https://example.com/pull/3", 0, false},
		{"", 0, false},
	}
	for _, tt := range tests {
		got, ok := githubPRNumber(tt.url)
		assert.Equal(t, tt.ok, ok, tt.url)
		assert.Equal(t, tt.want, got, tt.url)
	}
}

func mergedEvidence() *Evidence {
	ev := NewEvidence()
	ev.AddCommit("aaaa", "Merge pull request #12 from ndzuki/task/x")
	ev.AddPR(PREvidence{Number: 20, State: "MERGED", MergeCommit: &MergeCommitRef{OID: "bbbb"}})
	ev.AddCommit("bbbb", "feat: something")
	ev.AddPR(PREvidence{Number: 21, State: "OPEN"})
	return ev
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name         string
		card         Card
		wantKinds    []string
		wantSeverity []Severity
		wantVerified int
	}{
		{
			name:         "done card with a locally verifiable merge commit passes",
			card:         Card{Path: "TASK-1.md", Status: "done", MergeStatus: "merged", PRURL: "https://github.com/o/r/pull/12"},
			wantVerified: 1,
		},
		{
			name:         "closed card verified through gh merge commit passes",
			card:         Card{Path: "TASK-2.md", Status: "closed", MergeStatus: "merged", PRURL: "https://github.com/o/r/pull/20"},
			wantVerified: 1,
		},
		{
			name:         "done card with merge_status unmerged is a violation",
			card:         Card{Path: "TASK-3.md", Status: "done", MergeStatus: "unmerged", PRURL: "https://github.com/o/r/pull/12"},
			wantKinds:    []string{KindMergeStatusMismatch},
			wantSeverity: []Severity{SeverityViolation},
		},
		{
			name:         "closed card without pr_url is a violation",
			card:         Card{Path: "TASK-4.md", Status: "closed", MergeStatus: "merged", PRURL: ""},
			wantKinds:    []string{KindMissingPRURL},
			wantSeverity: []Severity{SeverityViolation},
		},
		{
			name:         "PR that gh reports OPEN is a violation",
			card:         Card{Path: "TASK-5.md", Status: "done", MergeStatus: "merged", PRURL: "https://github.com/o/r/pull/21"},
			wantKinds:    []string{KindPRNotMerged},
			wantSeverity: []Severity{SeverityViolation},
		},
		{
			name:         "PR with no evidence anywhere is unverified",
			card:         Card{Path: "TASK-6.md", Status: "done", MergeStatus: "merged", PRURL: "https://github.com/o/r/pull/999"},
			wantKinds:    []string{KindPRUnverified},
			wantSeverity: []Severity{SeverityUnverified},
		},
		{
			name:         "non-GitHub pr_url is unverified",
			card:         Card{Path: "TASK-7.md", Status: "done", MergeStatus: "merged", PRURL: "https://gitlab.example.com/o/r/-/merge_requests/3"},
			wantKinds:    []string{KindPRUnverified},
			wantSeverity: []Severity{SeverityUnverified},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Check([]Card{tt.card}, mergedEvidence())
			require.Len(t, result.Findings, len(tt.wantKinds))
			for i, want := range tt.wantKinds {
				assert.Equal(t, want, result.Findings[i].Kind)
				assert.Equal(t, tt.wantSeverity[i], result.Findings[i].Severity)
			}
			assert.Equal(t, tt.wantVerified, result.Verified)
		})
	}
}

func TestCheckSkipsCardsThatAreNotDoneOrClosed(t *testing.T) {
	for _, status := range []string{"ready", "in-progress", "superseded", "draft"} {
		result := Check([]Card{{Path: "TASK-8.md", Status: status}}, NewEvidence())
		assert.Equal(t, 0, result.Checked, status)
		assert.Empty(t, result.Findings, status)
	}
}

// TestCheckNegativeControlDoneButUnmerged is the regression the gate exists for:
// a card that claims a completed delivery while its frontmatter admits the
// change was never merged must be reported. If the merge_status assertion is
// removed from Check, this test fails -- the mutation is observable.
func TestCheckNegativeControlDoneButUnmerged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "TASK-902-mutated.md")
	require.NoError(t, os.WriteFile(path, []byte(`---
status: done
merge_status: unmerged
pr_url: "https://github.com/ndzuki/release-manager/pull/12"
---
`), 0o600))

	card, err := ParseCard(path)
	require.NoError(t, err)

	result := Check([]Card{card}, mergedEvidence())
	require.Len(t, result.Findings, 1)
	assert.Equal(t, KindMergeStatusMismatch, result.Findings[0].Kind)
	assert.Equal(t, SeverityViolation, result.Findings[0].Severity)
	assert.True(t, result.Failed(Options{}), "a ledger contradiction must fail even when unverified evidence is allowed")
	assert.True(t, result.Failed(Options{AllowUnverified: true}))
}

func TestFailedUnverifiedEscapeHatch(t *testing.T) {
	card := Card{Path: "TASK-9.md", Status: "done", MergeStatus: "merged", PRURL: "https://github.com/o/r/pull/999"}
	result := Check([]Card{card}, NewEvidence())
	require.True(t, result.Failed(Options{}), "unverified must fail by default")
	assert.False(t, result.Failed(Options{AllowUnverified: true}), "ALLOW_UNVERIFIED_TASKS=1 downgrades unverified to advisory")
}

func TestFailedPassesWhenEverythingVerified(t *testing.T) {
	card := Card{Path: "TASK-10.md", Status: "done", MergeStatus: "merged", PRURL: "https://github.com/o/r/pull/12"}
	result := Check([]Card{card}, mergedEvidence())
	assert.False(t, result.Failed(Options{}))
}
