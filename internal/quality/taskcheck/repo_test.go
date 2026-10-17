package taskcheck

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRepoSlugEqual(t *testing.T) {
	t.Parallel()

	base := RepoSlug{Owner: "ndzuki", Name: "release-manager"}
	assert.True(t, base.Equal(RepoSlug{Owner: "ndzuki", Name: "release-manager"}))
	assert.True(t, base.Equal(RepoSlug{Owner: "NDZUKI", Name: "Release-Manager"}), "GitHub folds case")
	assert.False(t, base.Equal(RepoSlug{Owner: "ndzuki", Name: "release-manager-evil"}), "a name that merely starts with a repository's name is a different repository")
	assert.False(t, base.Equal(RepoSlug{Owner: "someone", Name: "release-manager"}))
	assert.False(t, base.Equal(RepoSlug{}))
}

func TestRepoSlugIsZeroAndString(t *testing.T) {
	t.Parallel()

	assert.True(t, RepoSlug{}.IsZero())
	assert.True(t, RepoSlug{Owner: "ndzuki"}.IsZero())
	assert.True(t, RepoSlug{Name: "release-manager"}.IsZero())
	assert.Equal(t, "", RepoSlug{}.String())
	assert.Equal(t, "ndzuki/release-manager", RepoSlug{Owner: "ndzuki", Name: "release-manager"}.String())
}

func TestParseRepoSlug(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want RepoSlug
		ok   bool
	}{
		{"https no suffix", "https://github.com/ndzuki/release-manager", RepoSlug{Owner: "ndzuki", Name: "release-manager"}, true},
		{"https with .git", "https://github.com/ndzuki/release-manager.git", RepoSlug{Owner: "ndzuki", Name: "release-manager"}, true},
		{"https trailing slash", "https://github.com/ndzuki/release-manager/", RepoSlug{Owner: "ndzuki", Name: "release-manager"}, true},
		{"https with userinfo", "https://user@github.com/ndzuki/release-manager.git", RepoSlug{Owner: "ndzuki", Name: "release-manager"}, true},
		{"www host", "https://www.github.com/ndzuki/release-manager.git", RepoSlug{Owner: "ndzuki", Name: "release-manager"}, true},
		{"host and path case are folded", "https://GitHub.com/NDZUKI/Release-Manager", RepoSlug{Owner: "ndzuki", Name: "release-manager"}, true},
		{"scp-like", "git@github.com:ndzuki/release-manager.git", RepoSlug{Owner: "ndzuki", Name: "release-manager"}, true},
		{"scp-like without .git", "git@github.com:ndzuki/release-manager", RepoSlug{Owner: "ndzuki", Name: "release-manager"}, true},
		{"ssh with port", "ssh://git@github.com:22/ndzuki/release-manager.git", RepoSlug{Owner: "ndzuki", Name: "release-manager"}, true},
		{"git protocol", "git://github.com/ndzuki/release-manager.git", RepoSlug{Owner: "ndzuki", Name: "release-manager"}, true},
		{"bare host/path", "github.com/ndzuki/release-manager", RepoSlug{Owner: "ndzuki", Name: "release-manager"}, true},
		{"surrounding whitespace", "  https://github.com/ndzuki/release-manager  ", RepoSlug{Owner: "ndzuki", Name: "release-manager"}, true},
		{"suffix name is kept whole", "https://github.com/ndzuki/release-manager-evil.git", RepoSlug{Owner: "ndzuki", Name: "release-manager-evil"}, true},
		{"other host is not GitHub", "https://gitlab.com/ndzuki/release-manager.git", RepoSlug{}, false},
		{"scp-like other host", "git@gitlab.com:ndzuki/release-manager.git", RepoSlug{}, false},
		{"host suffix attack is not GitHub", "https://github.com.evil.example/ndzuki/release-manager", RepoSlug{}, false},
		{"local path", "/home/nd/src/repos/github.com/ndzuki/release-manager", RepoSlug{}, false},
		{"owner only", "https://github.com/ndzuki", RepoSlug{}, false},
		{"empty path", "https://github.com/", RepoSlug{}, false},
		{"deeper path", "https://github.com/ndzuki/release-manager/tree/main", RepoSlug{}, false},
		{"file URL", "file:///home/nd/release-manager", RepoSlug{}, false},
		{"empty", "", RepoSlug{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := ParseRepoSlug(tt.raw)
			assert.Equal(t, tt.ok, ok, tt.raw)
			if tt.ok {
				assert.True(t, tt.want.Equal(got), "want %s, got %s", tt.want, got)
			}
		})
	}
}
