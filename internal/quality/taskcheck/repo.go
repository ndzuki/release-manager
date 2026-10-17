package taskcheck

import (
	"net/url"
	"strings"
)

// RepoSlug is the owner/name identity of a GitHub repository, e.g.
// "ndzuki/release-manager". GitHub folds owner and repository names
// case-insensitively, so Equal compares them that way.
//
// Why this type exists: the ledger gate used to read only the pull request
// NUMBER out of pr_url, so a card could cite any repository's PR whose number
// happened to collide with a merged PR of this one and be accepted. Comparing
// owner/repo is what makes the number mean something.
type RepoSlug struct {
	Owner string
	Name  string
}

// IsZero reports whether the slug names no repository.
func (s RepoSlug) IsZero() bool {
	return s.Owner == "" || s.Name == ""
}

// String renders the canonical "owner/name" form, or "" for the zero slug.
func (s RepoSlug) String() string {
	if s.IsZero() {
		return ""
	}
	return s.Owner + "/" + s.Name
}

// Equal reports whether both slugs name the same repository. Comparison folds
// case because GitHub does, and it is never a prefix comparison: "release-manager-evil" is a
// different repository from "release-manager", not a variant of it.
func (s RepoSlug) Equal(other RepoSlug) bool {
	return strings.EqualFold(s.Owner, other.Owner) && strings.EqualFold(s.Name, other.Name)
}

// ParseRepoSlug derives the GitHub owner/name a git remote URL points at. It
// understands the forms `git remote get-url origin` can print:
//
//	https://github.com/owner/repo.git
//	https://user@github.com/owner/repo
//	ssh://git@github.com:22/owner/repo.git
//	git@github.com:owner/repo.git
//	github.com/owner/repo
//
// A host that is not GitHub (the PR channel is GitHub-only) or a path that does
// not name exactly one owner/repo pair yields ok=false, and the caller must then
// treat pull request URLs as unverifiable rather than assume they belong here.
func ParseRepoSlug(raw string) (RepoSlug, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return RepoSlug{}, false
	}
	// URL forms (https://, ssh://, git://, git+ssh://), with optional userinfo and port.
	if u, err := url.Parse(s); err == nil && u.Scheme != "" {
		return slugFromHostPath(u.Hostname(), u.Path)
	}
	// scp-like form git uses for its own transport: [user@]github.com:owner/repo.git
	if host, path, ok := strings.Cut(s, ":"); ok {
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
		return slugFromHostPath(host, path)
	}
	// Bare "github.com/owner/repo".
	host, path, ok := strings.Cut(s, "/")
	if !ok {
		return RepoSlug{}, false
	}
	return slugFromHostPath(host, path)
}

// slugFromHostPath builds the slug for a host/path pair, rejecting everything
// that is not exactly one owner and one repository on GitHub.
func slugFromHostPath(host, path string) (RepoSlug, bool) {
	host = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(host)), "www.")
	if host != "github.com" {
		return RepoSlug{}, false
	}
	segments := strings.Split(strings.Trim(strings.TrimSpace(path), "/"), "/")
	if len(segments) != 2 {
		return RepoSlug{}, false
	}
	owner := strings.ToLower(segments[0])
	name := strings.ToLower(trimGitSuffix(segments[1]))
	if owner == "" || name == "" {
		return RepoSlug{}, false
	}
	return RepoSlug{Owner: owner, Name: name}, true
}

// trimGitSuffix removes one trailing ".git", the suffix git clone URLs carry and
// GitHub repository names never do. The comparison folds case because GitHub's
// repository names do.
func trimGitSuffix(name string) string {
	if len(name) >= 4 && strings.EqualFold(name[len(name)-4:], ".git") {
		return name[:len(name)-4]
	}
	return name
}
