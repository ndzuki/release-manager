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

func TestGithubPRRef(t *testing.T) {
	repo := RepoSlug{Owner: "ndzuki", Name: "release-manager"}
	tests := []struct {
		url  string
		want PRRef
		ok   bool
	}{
		{url: "https://github.com/ndzuki/release-manager/pull/101", want: PRRef{Repo: repo, Number: 101}, ok: true},
		{url: "https://github.com/ndzuki/release-manager/pull/7/files", want: PRRef{Repo: repo, Number: 7}, ok: true},
		{url: "http://github.com/o/r/pull/3", want: PRRef{Repo: RepoSlug{Owner: "o", Name: "r"}, Number: 3}, ok: true},
		{url: "https://www.github.com/o/r/pull/3/", want: PRRef{Repo: RepoSlug{Owner: "o", Name: "r"}, Number: 3}, ok: true},
		{url: "https://github.com/O/R.git/pull/3?diff=split#top", want: PRRef{Repo: RepoSlug{Owner: "o", Name: "r"}, Number: 3}, ok: true},
		// A longer name is its own repository, not this one: no prefix matching.
		{url: "https://github.com/o/release-manager-evil/pull/3", want: PRRef{Repo: RepoSlug{Owner: "o", Name: "release-manager-evil"}, Number: 3}, ok: true},
		{url: "https://example.com/pull/3", ok: false},
		{url: "https://github.com/o/r/pull/abc", ok: false},
		{url: "https://github.com/o/r/commit/abc", ok: false},
		{url: "", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got, ok := githubPRRef(tt.url)
			assert.Equal(t, tt.ok, ok, tt.url)
			assert.Equal(t, tt.want, got, tt.url)
		})
	}
}

func TestGithubPRRefs(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []PRRef
		ok   bool
	}{
		{"single", "https://github.com/o/r/pull/5", []PRRef{{Repo: RepoSlug{Owner: "o", Name: "r"}, Number: 5}}, true},
		{"comma separated", "https://github.com/o/r/pull/5,https://github.com/o/r/pull/7", []PRRef{
			{Repo: RepoSlug{Owner: "o", Name: "r"}, Number: 5},
			{Repo: RepoSlug{Owner: "o", Name: "r"}, Number: 7},
		}, true},
		{"mixed separators keep the order", "https://github.com/o/r/pull/7; https://github.com/o/r/pull/5", []PRRef{
			{Repo: RepoSlug{Owner: "o", Name: "r"}, Number: 7},
			{Repo: RepoSlug{Owner: "o", Name: "r"}, Number: 5},
		}, true},
		{"each URL keeps its own repository", "https://github.com/o/r/pull/5, https://github.com/other/repo/pull/7", []PRRef{
			{Repo: RepoSlug{Owner: "o", Name: "r"}, Number: 5},
			{Repo: RepoSlug{Owner: "other", Name: "repo"}, Number: 7},
		}, true},
		{"one bad segment invalidates the value", "https://github.com/o/r/pull/5, nope", nil, false},
		{"empty", "", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := githubPRRefs(tt.raw)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func mergedEvidence() *Evidence {
	ev := NewEvidence()
	ev.SetRepoSlug(RepoSlug{Owner: "o", Name: "r"})
	ev.AddCommit("aaaa", "Merge pull request #12 from ndzuki/task/x")
	ev.AddPR(PREvidence{Number: 20, URL: "https://github.com/o/r/pull/20", State: "MERGED", MergeCommit: &MergeCommitRef{OID: "bbbb"}})
	ev.AddCommit("bbbb", "feat: something")
	ev.AddPR(PREvidence{Number: 21, URL: "https://github.com/o/r/pull/21", State: "OPEN"})
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
		{
			// TASK-227: one delivery can land in two pull requests. Every listed PR needs its
			// own evidence, so both merged PRs verify the card.
			name:         "two merged PRs both verified pass",
			card:         Card{Path: "TASK-8.md", Status: "done", MergeStatus: "merged", PRURL: "https://github.com/o/r/pull/12, https://github.com/o/r/pull/20"},
			wantVerified: 1,
		},
		{
			name:         "the second PR having no evidence fails",
			card:         Card{Path: "TASK-9.md", Status: "done", MergeStatus: "merged", PRURL: "https://github.com/o/r/pull/12 https://github.com/o/r/pull/999"},
			wantKinds:    []string{KindPRUnverified},
			wantSeverity: []Severity{SeverityUnverified},
		},
		{
			name:         "the second PR being open is a violation",
			card:         Card{Path: "TASK-10.md", Status: "done", MergeStatus: "merged", PRURL: "https://github.com/o/r/pull/12; https://github.com/o/r/pull/21"},
			wantKinds:    []string{KindPRNotMerged},
			wantSeverity: []Severity{SeverityViolation},
		},
		{
			// A malformed entry must not hide behind a good one.
			name:         "a non-GitHub entry makes the whole value unverifiable",
			card:         Card{Path: "TASK-11.md", Status: "done", MergeStatus: "merged", PRURL: "https://github.com/o/r/pull/12 https://gitlab.example.com/o/r/-/merge_requests/3"},
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

// The hole these tests close: a card could cite ANY repository's pull request and pass
// whenever the number collided with a merged PR of this one. The review of TASK-327
// reproduced https://github.com/torvalds/linux/pull/1 passing against this repository's
// merged PR #1, because only the number was read out of the URL. Identity is a
// contradiction, not missing evidence, so the escape hatch must not downgrade it.
func TestCheck_RejectsPRFromAnotherRepository(t *testing.T) {
	t.Parallel()

	card := Card{Path: "TASK-920-foreign.md", Status: "done", MergeStatus: "merged",
		PRURL: "https://github.com/torvalds/linux/pull/12"}
	result := Check([]Card{card}, mergedEvidence())

	require.Len(t, result.Findings, 1)
	assert.Equal(t, KindForeignRepo, result.Findings[0].Kind)
	assert.Equal(t, SeverityViolation, result.Findings[0].Severity)
	assert.Contains(t, result.Findings[0].Message, "torvalds/linux")
	assert.Contains(t, result.Findings[0].Message, "o/r")
	assert.Zero(t, result.Verified)
	assert.True(t, result.Failed(Options{}))
	assert.True(t, result.Failed(Options{AllowUnverified: true}),
		"ALLOW_UNVERIFIED_TASKS must never downgrade a foreign repository")
}

// Every spelling of the same borrowed pull request is still borrowed. The last case is the
// forged-prefix shape: "release-manager-evil" merely starts with this repository's name.
func TestCheck_ForeignRepoVariantsCannotBorrow(t *testing.T) {
	t.Parallel()

	foreign := []string{
		"https://github.com/torvalds/linux/pull/12",
		"https://GitHub.com/Torvalds/Linux/pull/12",
		"https://www.github.com/torvalds/linux/pull/12",
		"https://github.com/torvalds/linux.git/pull/12",
		"https://github.com/torvalds/linux/pull/12/",
		"https://github.com/torvalds/linux/pull/12?diff=split",
		"https://github.com/torvalds/linux/pull/12#issuecomment-1",
		"https://github.com/torvalds/linux/pull/12/files",
		"https://github.com/o/release-manager-evil/pull/12",
		"https://github.com/o/r/pull/12, https://github.com/torvalds/linux/pull/13",
	}
	for _, url := range foreign {
		t.Run(url, func(t *testing.T) {
			t.Parallel()

			card := Card{Path: "TASK-921-variant.md", Status: "done", MergeStatus: "merged", PRURL: url}
			result := Check([]Card{card}, mergedEvidence())

			require.Len(t, result.Findings, 1, url)
			assert.Equal(t, KindForeignRepo, result.Findings[0].Kind, url)
			assert.Equal(t, SeverityViolation, result.Findings[0].Severity, url)
			assert.True(t, result.Failed(Options{AllowUnverified: true}), url)
		})
	}
}

// The mirror image: every spelling of a pull request IN this repository that the parser
// accepts still verifies, so tightening identity does not break the canonical forms.
func TestCheck_OwnRepoVariantsStillVerify(t *testing.T) {
	t.Parallel()

	for _, url := range []string{
		"https://github.com/o/r/pull/12",
		"https://GitHub.com/O/R/pull/12",
		"https://www.github.com/o/r/pull/12/",
		"https://github.com/o/r.git/pull/12?diff=split#top",
	} {
		t.Run(url, func(t *testing.T) {
			t.Parallel()

			card := Card{Path: "TASK-922-own.md", Status: "done", MergeStatus: "merged", PRURL: url}
			result := Check([]Card{card}, mergedEvidence())

			assert.Empty(t, result.Findings, url)
			assert.Equal(t, 1, result.Verified, url)
		})
	}
}

// When the caller cannot say which repository this is, no PR URL can be attributed. The
// evidence that would otherwise verify the card is deliberately present, so this fails only
// if the repository identity is missing: unverified, never a silent pass.
func TestCheck_UnknownRepositoryCannotVerifyPRURLs(t *testing.T) {
	t.Parallel()

	ev := NewEvidence()
	ev.AddCommit("aaaa", "Merge pull request #12 from ndzuki/task/x")
	card := Card{Path: "TASK-923-no-repo.md", Status: "done", MergeStatus: "merged",
		PRURL: "https://github.com/o/r/pull/12"}
	result := Check([]Card{card}, ev)

	require.Len(t, result.Findings, 1)
	assert.Equal(t, KindPRUnverified, result.Findings[0].Kind)
	assert.Equal(t, SeverityUnverified, result.Findings[0].Severity)
	assert.Contains(t, result.Findings[0].Message, "owner/repo is unknown")
	assert.Zero(t, result.Verified)
	assert.True(t, result.Failed(Options{}))
	assert.False(t, result.Failed(Options{AllowUnverified: true}), "still downgradable by the explicit hatch")
}

// gh's url is the online evidence's own identity: evidence gh attributed to another
// repository must not verify a card even when that PR's merge commit is reachable locally
// (a fork or mirror can share history).
func TestCheck_RejectsGHEvidenceFromAnotherRepository(t *testing.T) {
	t.Parallel()

	ev := mergedEvidence()
	ev.AddPR(PREvidence{Number: 30, URL: "https://github.com/other/repo/pull/30",
		State: "MERGED", MergeCommit: &MergeCommitRef{OID: "bbbb"}})
	card := Card{Path: "TASK-924-foreign-gh.md", Status: "done", MergeStatus: "merged",
		PRURL: "https://github.com/o/r/pull/30"}
	result := Check([]Card{card}, ev)

	require.Len(t, result.Findings, 1)
	assert.Equal(t, KindForeignRepo, result.Findings[0].Kind)
	assert.Equal(t, SeverityViolation, result.Findings[0].Severity)
	assert.Contains(t, result.Findings[0].Message, "other/repo")
	assert.True(t, result.Failed(Options{AllowUnverified: true}))
}

// A gh row whose url cannot be used is not evidence for the number that was looked up:
// unparsable or describing a different number fails closed, while an absent url (gh output
// that carries no attribution) is skipped because the card-side repository check still
// applies.
func TestCheck_GHEvidenceURLHandling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		url      string
		wantKind string
	}{
		{"unparsable url", "not-a-url", KindPRUnverified},
		{"url describes another number", "https://github.com/o/r/pull/31", KindPRUnverified},
		{"absent url is skipped", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ev := mergedEvidence()
			ev.AddPR(PREvidence{Number: 30, URL: tt.url, State: "MERGED",
				MergeCommit: &MergeCommitRef{OID: "bbbb"}})
			card := Card{Path: "TASK-925-gh-url.md", Status: "done", MergeStatus: "merged",
				PRURL: "https://github.com/o/r/pull/30"}
			result := Check([]Card{card}, ev)

			if tt.url == "" {
				assert.Empty(t, result.Findings)
				assert.Equal(t, 1, result.Verified)
				return
			}
			require.Len(t, result.Findings, 1)
			assert.Equal(t, tt.wantKind, result.Findings[0].Kind)
			assert.Equal(t, SeverityUnverified, result.Findings[0].Severity)
			assert.Zero(t, result.Verified)
		})
	}
}

// A `direct:` value that swallows a second ref must be a malformed-pointer violation, not a
// bogus repository path that merely fails to verify. Before ParseDirectRef rejected the
// embedded comma, "direct:<sha1>@/repo,direct:<sha2>@/repo" produced ONE ref with the repo
// "/repo,direct:<sha2>@/repo", whose absence from the evidence file made the card UNVERIFIED
// -- and ALLOW_UNVERIFIED_TASKS=1 then passed it.
func TestCheck_MultipleDirectRefsIsAMalformedPointerViolation(t *testing.T) {
	t.Parallel()

	card := directCard("direct:"+directSHA+"@"+directRepo+",direct:"+directOther+"@"+directRepo, directPath)
	result := Check([]Card{card}, directEvidence())

	require.Len(t, result.Findings, 1)
	assert.Equal(t, KindDirectEvidenceInvalid, result.Findings[0].Kind)
	assert.Equal(t, SeverityViolation, result.Findings[0].Severity)
	assert.Zero(t, result.Verified)
	assert.True(t, result.Failed(Options{}))
	assert.True(t, result.Failed(Options{AllowUnverified: true}),
		"ALLOW_UNVERIFIED_TASKS must not downgrade a malformed direct pointer")
	assert.Empty(t, DirectRefs([]Card{card}),
		"the collector must not be sent looking for a fabricated repository path")
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
pr_url: "https://github.com/o/r/pull/12"
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

// A card closed for a documented non-PR reason -- already implemented,
// superseded, or landed before this project used pull requests -- has no PR to
// verify, so the merge-evidence rule cannot apply; the stated reason is the
// record. The rule still bites when no reason is given. Removing the
// NonPRClosureReasons branch from Check makes this test fail.
func TestCheck_AcceptsDocumentedNonPRClosures(t *testing.T) {
	t.Parallel()

	for _, reason := range []string{"already-implemented", "superseded", "pre-pr-workflow"} {
		card := Card{Path: "TASK-900-" + reason + ".md", Status: "closed", ClosureReason: reason}
		result := Check([]Card{card}, NewEvidence())
		assert.Empty(t, result.Findings, "reason %q must be accepted", reason)
		assert.Equal(t, 1, result.Verified, "reason %q must count as verified", reason)
	}

	plain := Card{Path: "TASK-901-no-reason.md", Status: "closed"}
	plainResult := Check([]Card{plain}, NewEvidence())
	require.NotEmpty(t, plainResult.Findings, "without a reason the merge evidence is still required")
}

// Direct (non-PR) delivery channel: a delivery that lands in another repository
// whose automation commits and pushes straight to main (TASK-314/319/322), cited
// as `pr_url: direct:<sha>@<repo>` with `merge_status: direct`.

const (
	directRepo   = "~/src/dotfiles"
	directSHA    = "1111111111111111111111111111111111111111"
	directOther  = "3333333333333333333333333333333333333333"
	directPath   = "plugins/vault-tools/ensure-time-marker.mjs"
	directTest   = "plugins/vault-tools/test/ensure-time-marker.test.mjs"
	directAbsent = "2222222222222222222222222222222222222222"
)

// directEvidence has two reachable commits in directRepo: directSHA changes the
// delivered path (plus its test, so a card can declare a subset), directOther
// changes an unrelated file.
func directEvidence() *Evidence {
	ev := NewEvidence()
	ev.AddDirectCommit(directRepo, directSHA)
	ev.AddDirectFile(directRepo, directSHA, directPath)
	ev.AddDirectFile(directRepo, directSHA, directTest)
	ev.AddDirectCommit(directRepo, directOther)
	ev.AddDirectFile(directRepo, directOther, "private_settings.yaml")
	return ev
}

func directCard(prURL string, paths ...string) Card {
	return Card{
		Path:          "TASK-314-ensure-time-marker-EXDEV.md",
		Status:        "done",
		MergeStatus:   "direct",
		PRURL:         prURL,
		EvidencePaths: paths,
	}
}

func TestParseDirectRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want DirectRef
		ok   bool
	}{
		{"absolute repo path", "direct:" + directSHA + "@/srv/dotfiles", DirectRef{Repo: "/srv/dotfiles", SHA: directSHA}, true},
		{"home repo path", "direct:" + directSHA + "@" + directRepo, DirectRef{Repo: directRepo, SHA: directSHA}, true},
		{"surrounding spaces are tolerated", " direct:" + directSHA + "@" + directRepo + " ", DirectRef{Repo: directRepo, SHA: directSHA}, true},
		{"uppercase sha is normalised to lower case", "direct:" + strings.ToUpper(directSHA) + "@" + directRepo, DirectRef{Repo: directRepo, SHA: directSHA}, true},
		{"short sha", "direct:abc123@" + directRepo, DirectRef{}, false},
		{"no repo", "direct:" + directSHA, DirectRef{}, false},
		{"empty repo", "direct:" + directSHA + "@", DirectRef{}, false},
		{"relative repo is not a resolvable identity", "direct:" + directSHA + "@dotfiles", DirectRef{}, false},
		{"repo with whitespace", "direct:" + directSHA + "@~/src/dot files", DirectRef{}, false},
		{"two refs separated by a comma are not one ref", "direct:" + directSHA + "@" + directRepo + ",direct:" + directOther + "@" + directRepo, DirectRef{}, false},
		{"a trailing comma is not part of a repo path", "direct:" + directSHA + "@" + directRepo + ",", DirectRef{}, false},
		{"an embedded direct: scheme is not a repo path", "direct:" + directSHA + "@/tmp/direct:" + directOther, DirectRef{}, false},
		{"an embedded newline is not a repo path", "direct:" + directSHA + "@" + directRepo + "\ndirect:" + directOther, DirectRef{}, false},
		{"a plain PR URL is not a direct ref", "https://github.com/o/r/pull/1", DirectRef{}, false},
		{"empty", "", DirectRef{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ParseDirectRef(tt.raw)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseDirectEvidence(t *testing.T) {
	t.Parallel()

	ev := NewEvidence()
	require.NoError(t, ev.ParseDirectEvidence(strings.NewReader(
		"# comment\n"+
			"\n"+
			"commit\t"+directRepo+"\t"+directSHA+"\n"+
			"file\t"+directRepo+"\t"+directSHA+"\t"+directPath+"\n"+
			"file\t"+directRepo+"\t"+directSHA+"\t"+directTest+"\n")))

	ref := DirectRef{Repo: directRepo, SHA: directSHA}
	assert.True(t, ev.HasDirectCommit(ref))
	assert.Equal(t, map[string]struct{}{directPath: {}, directTest: {}}, ev.DirectFiles(ref))
	assert.False(t, ev.HasDirectCommit(DirectRef{Repo: directRepo, SHA: directAbsent}))

	// A malformed line is an error: the file is our own collector's output, so a
	// line it cannot parse means the collection step is broken. Dropping it would
	// silently turn "broken collector" into "no evidence".
	broken := NewEvidence()
	require.Error(t, broken.ParseDirectEvidence(strings.NewReader("commit\t"+directRepo+"\n")))
	require.Error(t, broken.ParseDirectEvidence(strings.NewReader("nonsense\t"+directRepo+"\t"+directSHA+"\n")))
	require.Empty(t, broken.directCommits, "a malformed line must not half-populate the evidence")

	empty := NewEvidence()
	require.NoError(t, empty.ParseDirectEvidence(strings.NewReader("  \n")))
	assert.Empty(t, empty.directCommits)
}

func TestDirectRefs(t *testing.T) {
	t.Parallel()

	cards := []Card{
		directCard("direct:"+directSHA+"@"+directRepo, directPath),
		directCard("direct:"+directSHA+"@"+directRepo, directPath), // duplicate collapses
		directCard("direct:"+directOther+"@"+directRepo, directPath),
		directCard("https://github.com/o/r/pull/1", directPath), // PR card, not a direct ref
		directCard("direct:not-a-sha@"+directRepo, directPath),  // malformed: Check reports it
	}
	assert.Equal(t, []DirectRef{
		{Repo: directRepo, SHA: directSHA},
		{Repo: directRepo, SHA: directOther},
	}, DirectRefs(cards))
}

func TestCheck_DirectChannel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		card         Card
		wantKinds    []string
		wantSeverity []Severity
		wantVerified int
	}{
		{
			// The watcher commits in batches ("sync N file(s)"), so the commit
			// changes more than the card delivered: a subset declaration is
			// honest evidence, and the batch must not make the card fail.
			name:         "a reachable commit covering every declared path verifies the card",
			card:         directCard("direct:"+directSHA+"@"+directRepo, directPath),
			wantVerified: 1,
		},
		{
			name:         "declaring every path the commit changed also verifies",
			card:         directCard("direct:"+directSHA+"@"+directRepo, directPath, directTest),
			wantVerified: 1,
		},
		{
			name:         "a sha the repository does not have is unverified",
			card:         directCard("direct:"+directAbsent+"@"+directRepo, directPath),
			wantKinds:    []string{KindDirectEvidenceUnverified},
			wantSeverity: []Severity{SeverityUnverified},
		},
		{
			name:         "a repository this checkout has no evidence for is unverified",
			card:         directCard("direct:"+directSHA+"@~/src/elsewhere", directPath),
			wantKinds:    []string{KindDirectEvidenceUnverified},
			wantSeverity: []Severity{SeverityUnverified},
		},
		{
			// The mutation that matters: the sha is real and reachable, but the
			// commit did not touch what the card claims to have delivered.
			name:         "a reachable commit that changes other files is a violation",
			card:         directCard("direct:"+directOther+"@"+directRepo, directPath),
			wantKinds:    []string{KindDirectPathsMismatch},
			wantSeverity: []Severity{SeverityViolation},
		},
		{
			name:         "one missing path out of two is a violation",
			card:         directCard("direct:"+directSHA+"@"+directRepo, directPath, "plugins/vault-tools/other.mjs"),
			wantKinds:    []string{KindDirectPathsMismatch},
			wantSeverity: []Severity{SeverityViolation},
		},
		{
			name:         "no evidence_paths is a violation",
			card:         directCard("direct:" + directSHA + "@" + directRepo),
			wantKinds:    []string{KindDirectEvidenceInvalid},
			wantSeverity: []Severity{SeverityViolation},
		},
		{
			name:         "an absolute evidence path can never be matched, so it is a violation",
			card:         directCard("direct:"+directSHA+"@"+directRepo, "/etc/passwd"),
			wantKinds:    []string{KindDirectEvidenceInvalid},
			wantSeverity: []Severity{SeverityViolation},
		},
		{
			name:         "a parent-escape evidence path is a violation",
			card:         directCard("direct:"+directSHA+"@"+directRepo, "../../etc/passwd"),
			wantKinds:    []string{KindDirectEvidenceInvalid},
			wantSeverity: []Severity{SeverityViolation},
		},
		{
			name:         "a malformed sha is a violation, not an unverified card",
			card:         directCard("direct:xyz@"+directRepo, directPath),
			wantKinds:    []string{KindDirectEvidenceInvalid},
			wantSeverity: []Severity{SeverityViolation},
		},
		{
			name:         "a PR url under merge_status=direct is a violation",
			card:         directCard("https://github.com/o/r/pull/12", directPath),
			wantKinds:    []string{KindDirectEvidenceInvalid},
			wantSeverity: []Severity{SeverityViolation},
		},
		{
			name:         "an empty pr_url is still the plain missing_pr_url violation",
			card:         directCard("", directPath),
			wantKinds:    []string{KindMissingPRURL},
			wantSeverity: []Severity{SeverityViolation},
		},
		{
			name: "merge_status=merged citing a direct commit is a channel mismatch",
			card: Card{
				Path: "TASK-315.md", Status: "done", MergeStatus: "merged",
				PRURL: "direct:" + directSHA + "@" + directRepo, EvidencePaths: []string{directPath},
			},
			wantKinds:    []string{KindEvidenceChannelMismatch},
			wantSeverity: []Severity{SeverityViolation},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := Check([]Card{tt.card}, directEvidence())
			require.Len(t, result.Findings, len(tt.wantKinds))
			for i, want := range tt.wantKinds {
				assert.Equal(t, want, result.Findings[i].Kind)
				assert.Equal(t, tt.wantSeverity[i], result.Findings[i].Severity)
			}
			assert.Equal(t, tt.wantVerified, result.Verified)
		})
	}
}

// The direct channel must not become a quieter way to claim a delivery: an
// explicit ALLOW_UNVERIFIED_TASKS=1 may only relax MISSING evidence, exactly as
// it does on the PR path. Every contradiction stays fatal.
func TestCheck_DirectChannelViolationsSurviveAllowUnverified(t *testing.T) {
	t.Parallel()

	violations := []struct {
		name string
		card Card
	}{
		{"paths mismatch", directCard("direct:"+directOther+"@"+directRepo, directPath)},
		{"malformed sha", directCard("direct:xyz@"+directRepo, directPath)},
		{"no evidence_paths", directCard("direct:" + directSHA + "@" + directRepo)},
		{"wrong channel", directCard("https://github.com/o/r/pull/12", directPath)},
	}
	for _, tt := range violations {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := Check([]Card{tt.card}, directEvidence())
			require.Len(t, result.Findings, 1)
			assert.Equal(t, SeverityViolation, result.Findings[0].Severity)
			assert.True(t, result.Failed(Options{}))
			assert.True(t, result.Failed(Options{AllowUnverified: true}),
				"%s must stay fatal under ALLOW_UNVERIFIED_TASKS=1", result.Findings[0].Kind)
		})
	}

	// Only the card with no evidence anywhere is downgraded -- the same
	// behaviour the PR path has always had.
	missing := Check([]Card{directCard("direct:"+directAbsent+"@"+directRepo, directPath)}, directEvidence())
	require.Len(t, missing.Findings, 1)
	assert.Equal(t, SeverityUnverified, missing.Findings[0].Severity)
	assert.True(t, missing.Failed(Options{}))
	assert.False(t, missing.Failed(Options{AllowUnverified: true}))
}

// Negative control: without the changed-file-set comparison the gate would
// accept any reachable sha, which is exactly the "borrowed evidence" failure
// this channel exists to prevent. Removing the DirectFiles comparison in
// verifyDirectPaths makes this test fail.
func TestCheckNegativeControlDirectPathsMismatch(t *testing.T) {
	t.Parallel()

	card := directCard("direct:"+directOther+"@"+directRepo, directPath)
	result := Check([]Card{card}, directEvidence())
	require.Len(t, result.Findings, 1)
	assert.Equal(t, KindDirectPathsMismatch, result.Findings[0].Kind)
	assert.Equal(t, SeverityViolation, result.Findings[0].Severity)
	assert.True(t, result.Failed(Options{}))
	assert.True(t, result.Failed(Options{AllowUnverified: true}))
}

func TestParseCardEvidencePaths(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "TASK-903-direct.md")
	require.NoError(t, os.WriteFile(path, []byte(`---
status: done
merge_status: direct
pr_url: "direct:`+directSHA+`@`+directRepo+`"
evidence_paths: "plugins/a.mjs, plugins/b.mjs ,, plugins/c.mjs"
---
`), 0o600))

	card, err := ParseCard(path)
	require.NoError(t, err)
	assert.Equal(t, "direct", card.MergeStatus)
	assert.Equal(t, []string{"plugins/a.mjs", "plugins/b.mjs", "plugins/c.mjs"}, card.EvidencePaths,
		"empty segments are ignored, surrounding spaces are trimmed")
}
