package taskpremises

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The reference frame is fixed so the boundary case is exact: Now is midnight
// UTC and verified_at is a date-only value parsed in the same zone.
var (
	testNow    = time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	testMaxAge = 14 * 24 * time.Hour
)

// always maps exactly one sha to a count; every other sha is unknown.
func always(sha string, n int) func(string) (int, bool) {
	return func(candidate string) (int, bool) {
		if candidate == sha {
			return n, true
		}
		return 0, false
	}
}

func TestCheckFindings(t *testing.T) {
	fresh := Card{Path: "fresh.md", Status: "ready", VerifiedAt: "2026-10-01", VerifiedHead: "aaaa"}
	baseOpts := Options{Now: testNow, MaxAge: testMaxAge, Head: "headsha", CommitsAhead: always("aaaa", 0)}

	tests := []struct {
		name  string
		cards []Card
		opts  Options
		want  []string // expected kinds, in report order
	}{
		{
			name:  "fresh record has no findings",
			cards: []Card{fresh},
			opts:  baseOpts,
			want:  nil,
		},
		{
			name:  "exactly max age is still fresh (strictly greater-than)",
			cards: []Card{{Path: "edge.md", Status: "ready", VerifiedAt: "2026-09-21", VerifiedHead: "aaaa"}},
			opts:  baseOpts,
			want:  nil,
		},
		{
			name:  "one day past max age is stale",
			cards: []Card{{Path: "old.md", Status: "ready", VerifiedAt: "2026-09-20", VerifiedHead: "aaaa"}},
			opts:  baseOpts,
			want:  []string{KindStale},
		},
		{
			name:  "missing both fields is unrecorded",
			cards: []Card{{Path: "blank.md", Status: "ready"}},
			opts:  baseOpts,
			want:  []string{KindUnrecorded},
		},
		{
			name:  "missing verified_head is unrecorded",
			cards: []Card{{Path: "nohead.md", Status: "ready", VerifiedAt: "2026-10-01"}},
			opts:  baseOpts,
			want:  []string{KindUnrecorded},
		},
		{
			name:  "malformed verified_at is unrecorded",
			cards: []Card{{Path: "bad.md", Status: "ready", VerifiedAt: "2026/10/01", VerifiedHead: "aaaa"}},
			opts:  baseOpts,
			want:  []string{KindUnrecorded},
		},
		{
			name:  "HEAD ahead by N is head-moved",
			cards: []Card{fresh},
			opts:  Options{Now: testNow, MaxAge: testMaxAge, Head: "headsha", CommitsAhead: always("aaaa", 3)},
			want:  []string{KindHeadMoved},
		},
		{
			name:  "unknown verified_head is incomparable, never a guessed N",
			cards: []Card{fresh},
			opts:  Options{Now: testNow, MaxAge: testMaxAge, Head: "headsha", CommitsAhead: always("other", 0)},
			want:  []string{KindIncomparable},
		},
		{
			name:  "stale and head-moved are reported together",
			cards: []Card{{Path: "both.md", Status: "ready", VerifiedAt: "2026-09-01", VerifiedHead: "aaaa"}},
			opts:  Options{Now: testNow, MaxAge: testMaxAge, Head: "headsha", CommitsAhead: always("aaaa", 5)},
			want:  []string{KindHeadMoved, KindStale}, // sorted by kind within a path
		},
		{
			name:  "no -head skips the comparison instead of reporting incomparable",
			cards: []Card{fresh},
			opts:  Options{Now: testNow, MaxAge: testMaxAge, CommitsAhead: always("other", 0)},
			want:  nil,
		},
		{
			name:  "MaxAge <= 0 disables the staleness check",
			cards: []Card{{Path: "old.md", Status: "ready", VerifiedAt: "2020-01-01", VerifiedHead: "aaaa"}},
			opts:  Options{Now: testNow, Head: "headsha", CommitsAhead: always("aaaa", 0)},
			want:  nil,
		},
		{
			name:  "done card is ignored",
			cards: []Card{{Path: "done.md", Status: "done"}},
			opts:  baseOpts,
			want:  nil,
		},
		{
			name:  "in-progress card is ignored",
			cards: []Card{{Path: "wip.md", Status: "in-progress"}},
			opts:  baseOpts,
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := Check(tt.cards, tt.opts)
			var kinds []string
			for _, finding := range report.Findings {
				kinds = append(kinds, finding.Kind)
			}
			assert.Equal(t, tt.want, kinds)
		})
	}
}

func TestCheckCountsReadyAndSkipped(t *testing.T) {
	report := Check([]Card{
		{Path: "a.md", Status: "ready"},
		{Path: "b.md", Status: "done"},
		{Path: "c.md", Status: "ready"},
	}, Options{Now: testNow, MaxAge: testMaxAge})

	assert.Equal(t, 3, report.Cards)
	assert.Equal(t, 2, report.Checked)
	assert.Equal(t, 1, report.Skipped)
	assert.Equal(t, 2, report.Count(KindUnrecorded))
}

func TestHeadMovedMessageNamesTheCount(t *testing.T) {
	report := Check([]Card{
		{Path: "c.md", Status: "ready", VerifiedAt: "2026-10-01", VerifiedHead: "aaaa"},
	}, Options{Now: testNow, MaxAge: testMaxAge, Head: "headsha", CommitsAhead: always("aaaa", 7)})

	require.Len(t, report.Findings, 1)
	assert.Equal(t, KindHeadMoved, report.Findings[0].Kind)
	assert.Contains(t, report.Findings[0].Message, "moved 7 commit(s)")
	assert.Contains(t, report.Findings[0].Message, "aaaa")
}

func TestFindingsAreSortedByPathThenKind(t *testing.T) {
	report := Check([]Card{
		{Path: "z.md", Status: "ready"},
		{Path: "a.md", Status: "ready"},
	}, Options{Now: testNow, MaxAge: testMaxAge})

	require.Len(t, report.Findings, 2)
	assert.Equal(t, "a.md", report.Findings[0].Path)
	assert.Equal(t, "z.md", report.Findings[1].Path)
}

func TestParseCard(t *testing.T) {
	dir := t.TempDir()

	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		return path
	}

	t.Run("reads the subset and unquotes values", func(t *testing.T) {
		path := write("full.md", "---\nid: \"1\"\nstatus: \"ready\"\nverified_at: 2026-10-01\nverified_head: \"abc123\"\n---\n# body\n")
		card, err := ParseCard(path)
		require.NoError(t, err)
		assert.Equal(t, path, card.Path)
		assert.Equal(t, "ready", card.Status)
		assert.Equal(t, "2026-10-01", card.VerifiedAt)
		assert.Equal(t, "abc123", card.VerifiedHead)
		assert.True(t, card.Ready())
	})

	t.Run("optional fields stay empty", func(t *testing.T) {
		path := write("plain.md", "---\nstatus: done\n---\n")
		card, err := ParseCard(path)
		require.NoError(t, err)
		assert.Equal(t, "done", card.Status)
		assert.Empty(t, card.VerifiedAt)
		assert.Empty(t, card.VerifiedHead)
		assert.False(t, card.Ready())
	})

	t.Run("missing frontmatter is an error", func(t *testing.T) {
		path := write("naked.md", "# no frontmatter\n")
		_, err := ParseCard(path)
		require.Error(t, err)
	})
}
