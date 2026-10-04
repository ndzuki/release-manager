package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeCard(t *testing.T, dir, name, frontmatter string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(frontmatter), 0o600))
	return path
}

func today() string {
	return time.Now().UTC().Format("2006-01-02")
}

func TestRunReportsFindingsAndExitsZero(t *testing.T) {
	dir := t.TempDir()
	writeCard(t, dir, "TASK-900-ready.md", "---\nstatus: ready\n---\n")
	writeCard(t, dir, "TASK-901-done.md", "---\nstatus: done\n---\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{dir}, &stdout, &stderr)

	assert.Equal(t, 0, code, "findings must never change the exit code")
	out := stdout.String()
	assert.Contains(t, out, "2 card(s), 1 ready, 1 skipped")
	assert.Contains(t, out, "1 unrecorded")
	assert.Contains(t, out, "[unrecorded]")
	assert.Contains(t, out, "TASK-900-ready.md")
	assert.NotContains(t, out, "TASK-901-done.md", "a done card must not appear in findings")
}

func TestRunHeadMovedFromCommitsFile(t *testing.T) {
	dir := t.TempDir()
	card := writeCard(t, dir, "TASK-902-ready.md",
		"---\nstatus: ready\nverified_at: "+today()+"\nverified_head: deadbeef\n---\n")
	commits := filepath.Join(dir, "commits.tsv")
	require.NoError(t, os.WriteFile(commits, []byte("deadbeef\t2\n"), 0o600))

	var stdout, stderr bytes.Buffer
	code := run([]string{"-head", "cafebabe", "-commits", commits, card}, &stdout, &stderr)

	assert.Equal(t, 0, code)
	assert.Contains(t, stdout.String(), "1 head-moved")
	assert.Contains(t, stdout.String(), "moved 2 commit(s)")
}

func TestRunUnknownHeadIsIncomparableNotGuessed(t *testing.T) {
	dir := t.TempDir()
	card := writeCard(t, dir, "TASK-903-ready.md",
		"---\nstatus: ready\nverified_at: "+today()+"\nverified_head: vanish\n---\n")
	commits := filepath.Join(dir, "commits.tsv")
	require.NoError(t, os.WriteFile(commits, []byte("vanish\t-1\n"), 0o600))

	var stdout, stderr bytes.Buffer
	code := run([]string{"-head", "cafebabe", "-commits", commits, card}, &stdout, &stderr)

	assert.Equal(t, 0, code)
	assert.Contains(t, stdout.String(), "1 incomparable")
	assert.Contains(t, stdout.String(), "cannot count the commits")
	assert.NotContains(t, stdout.String(), "[head_moved]")
}

func TestRunPrintsAMoreLineAtTheLimit(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"TASK-910-a.md", "TASK-911-b.md", "TASK-912-c.md"} {
		writeCard(t, dir, name, "---\nstatus: ready\n---\n")
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"-max", "1", dir}, &stdout, &stderr)

	assert.Equal(t, 0, code)
	assert.Contains(t, stdout.String(), "... 2 more")
}

func TestRunWithoutCardsExitsTwo(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)

	assert.Equal(t, 2, code)
	assert.Contains(t, stderr.String(), "no task cards given")
}

func TestRunHelpExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-h"}, &stdout, &stderr)

	assert.Equal(t, 0, code)
	assert.Contains(t, stderr.String(), "usage: taskpremises")
	assert.Contains(t, stderr.String(), "-commits")
}

func TestRunUnreadableCardExitsTwo(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "TASK-999-gone.md")

	var stdout, stderr bytes.Buffer
	code := run([]string{missing}, &stdout, &stderr)

	assert.Equal(t, 2, code)
	assert.Contains(t, stderr.String(), "taskpremises:")
}

func TestRunUnreadableCommitsFileExitsTwo(t *testing.T) {
	dir := t.TempDir()
	card := writeCard(t, dir, "TASK-904-ready.md", "---\nstatus: ready\n---\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{"-head", "cafebabe", "-commits", filepath.Join(dir, "nope.tsv"), card}, &stdout, &stderr)

	assert.Equal(t, 2, code)
	assert.Contains(t, stderr.String(), "open commits file")
}

func TestRunSaysWhenHeadComparisonIsSkipped(t *testing.T) {
	dir := t.TempDir()
	card := writeCard(t, dir, "TASK-905-ready.md",
		"---\nstatus: ready\nverified_at: "+today()+"\nverified_head: deadbeef\n---\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{card}, &stdout, &stderr)

	assert.Equal(t, 0, code)
	assert.Contains(t, stdout.String(), "head comparison skipped (no -head given)")
}
