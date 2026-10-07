package docfacts

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testNginx = `    location ^~ /auth.v1. {
        proxy_pass http://auth:8085;
    }
    location ^~ /audit.v1. {
        proxy_pass http://api:8088;
    }
    location ^~ /trust.v1. {
        proxy_pass http://orchestrator:8083;
    }
`
	testVite = `    proxy: {
      '/auth.v1.': {
        target: 'http://127.0.0.1:8085',
      },
      '/audit.v1.': {
        target: 'http://127.0.0.1:8088',
      },
    },
`
	testHost = `DEV_PORTS=(8082 8083 8084 8085 8086 8087 8088)
`
)

func testFacts(t *testing.T) Facts {
	t.Helper()
	return NewFacts(testNginx, testVite, testHost, "deploy/dev/lib/host.sh")
}

func TestParseNginxPrefixes(t *testing.T) {
	prefixes, at := ParseNginxPrefixes(testNginx)
	assert.Equal(t, []string{"/audit.v1.", "/auth.v1.", "/trust.v1."}, prefixes)
	// Line numbers are 1-based and point at the location, so a finding can cite it.
	assert.Equal(t, 4, at["/audit.v1."])
	assert.Equal(t, 1, at["/auth.v1."])
	none, _ := ParseNginxPrefixes("# nothing here\n")
	assert.Empty(t, none)
}

func TestParseVitePrefixes(t *testing.T) {
	prefixes, at := ParseVitePrefixes(testVite)
	assert.Equal(t, []string{"/audit.v1.", "/auth.v1."}, prefixes)
	assert.Equal(t, 5, at["/audit.v1."], "line numbers are 1-based against the config file")
}

func TestParseDevPorts(t *testing.T) {
	low, high, ok := ParseDevPorts(testHost)
	require.True(t, ok)
	assert.Equal(t, 8082, low)
	assert.Equal(t, 8088, high)

	low, high, ok = ParseDevPorts("DEV_PORTS=(9090 9010)\n")
	require.True(t, ok)
	assert.Equal(t, 9010, low, "the band is min..max of the listed ports, not first..last")
	assert.Equal(t, 9090, high)

	_, _, ok = ParseDevPorts("nothing to see\n")
	assert.False(t, ok, "no band derived means the port rule stays silent")
}

// Rule A: prose that says a proxied prefix is missing. Removing this rule would
// fail this test, which is the point of pinning it here.
func TestCheckAbsentPrefix(t *testing.T) {
	facts := testFacts(t)
	violating := Item{Path: "docs/x.md", Line: 12, Text: "- **`/audit.v1.` 未被 nginx 代理**"}
	findings := Check([]Item{violating}, facts)
	require.Len(t, findings, 1)
	assert.Equal(t, "absent-prefix", findings[0].Rule)
	assert.Contains(t, findings[0].Message, "/audit.v1.")
	assert.Contains(t, findings[0].Message, "web/nginx.conf:4")
	assert.Contains(t, findings[0].String(), "docs/x.md:12")

	// A prefix the config does not proxy is not a contradiction.
	other := Item{Path: "docs/x.md", Line: 3, Text: "- `/notifier.v1.` 未被 nginx 代理"}
	assert.Empty(t, Check([]Item{other}, facts))

	// Saying it IS proxied is not an absence claim.
	positive := Item{Path: "docs/x.md", Line: 4, Text: "- `/audit.v1.` 现在有 nginx location"}
	assert.Empty(t, Check([]Item{positive}, facts))

	// Regression (found by running the audit on this repository): "没有" on a line
	// that names a prefix does not make it a routing claim. SECURITY.md says the
	// notifier and operator prefixes have no JWT/Casbin behind them.
	authGap := Item{Path: "SECURITY.md", Line: 405, Text: "| 入口把七个 proto 包前缀反向代理到集群内服务 | `/audit.v1.` 与 `/trust.v1.` 两条前缀后面没有 JWT/Casbin |"}
	assert.Empty(t, Check([]Item{authGap}, facts), "a missing-auth claim is not a missing-route claim")

	// A line that does negate routing near the prefix still reports.
	routeGap := Item{Path: "SECURITY.md", Line: 405, Text: "`/audit.v1.` 没有 location，未被 nginx 代理"}
	require.Len(t, Check([]Item{routeGap}, facts), 1)
}

// Rule B: a stated host port band that disagrees with DEV_PORTS.
func TestCheckPortBand(t *testing.T) {
	facts := testFacts(t)
	violating := Item{Path: "docs/runbook.md", Line: 537, Text: "# 2) 探针与元数据（宿主端口段 8082-8087）"}
	findings := Check([]Item{violating}, facts)
	require.Len(t, findings, 1)
	assert.Equal(t, "port-band", findings[0].Rule)
	assert.Contains(t, findings[0].Message, "8082-8088")
	assert.Contains(t, findings[0].Message, "deploy/dev/lib/host.sh")

	correct := Item{Path: "docs/runbook.md", Line: 537, Text: "# 2) 探针与元数据（宿主端口段 8082-8088）"}
	assert.Empty(t, Check([]Item{correct}, facts))

	// Review found the rule needed the literal "端口段": twelve lines in this
	// repository write host ports without that word.
	withoutLabel := Item{Path: "docs/user-manual.md", Line: 69, Text: "- 控制台入口：容器部署 8087；开发服务器代理到宿主端口 8082-8087"}
	findings = Check([]Item{withoutLabel}, facts)
	require.Len(t, findings, 1)
	assert.Equal(t, "port-band", findings[0].Rule)

	// Anchoring: a NodePort range next to the host band must not be parsed as 82-3008.
	withNodePort := Item{Path: "docs/x.md", Line: 40, Text: "宿主端口段 8082-8087:30082-30087"}
	findings = Check([]Item{withNodePort}, facts)
	require.Len(t, findings, 1)
	assert.Contains(t, findings[0].Message, "8082-8087")
	assert.NotContains(t, findings[0].Message, "82-3008")

	// A range that is not host ports at all is left alone.
	numericOnly := Item{Path: "docs/x.md", Line: 41, Text: "- 版本号区间 2024-2026"}
	assert.Empty(t, Check([]Item{numericOnly}, facts))

	// Without a derived band the rule must not guess one.
	noBand := NewFacts(testNginx, testVite, "no DEV_PORTS here\n", "deploy/dev/lib/host.sh")
	assert.Empty(t, Check([]Item{violating}, noBand))
}

// Rule C: an exclusivity claim whose enumeration is a strict subset.
func TestCheckExclusiveEnumeration(t *testing.T) {
	facts := testFacts(t)
	violating := Item{Path: "web/README.md", Line: 84, Text: "容器部署：`nginx.conf` 只反代 `/auth.v1.` 与 `/audit.v1.`"}
	findings := Check([]Item{violating}, facts)
	require.Len(t, findings, 1)
	assert.Equal(t, "exclusive-enumeration", findings[0].Rule)
	assert.Contains(t, findings[0].Message, "/trust.v1.")
	assert.Contains(t, findings[0].Message, "web/nginx.conf")

	full := Item{Path: "web/README.md", Line: 84, Text: "容器部署：`nginx.conf` 只反代 `/auth.v1.`、`/audit.v1.`、`/trust.v1.`"}
	assert.Empty(t, Check([]Item{full}, facts))

	// The Vite list is checked as its own subject.
	viteShort := Item{Path: "docs/x.md", Line: 9, Text: "vite.config.ts 只代理 `/auth.v1.`"}
	findings = Check([]Item{viteShort}, facts)
	require.Len(t, findings, 1)
	assert.Contains(t, findings[0].Message, "/audit.v1.")
}

// Historical qualifiers are the difference between a false positive and a finding:
// the B5 write-ups keep the pre-fix evidence on purpose.
func TestHistoricalClaimsAreExempt(t *testing.T) {
	facts := testFacts(t)
	cases := []struct {
		name string
		item Item
	}{
		{"same line", Item{Path: "docs/user-manual.md", Line: 234, Text: "上图是**修复前**的失败态：`/audit.v1.` 未被 nginx 代理"}},
		{"line above", Item{Path: "docs/ux-review.md", Line: 145, Text: "- 容器入口：`/audit.v1.` 未被 nginx 代理", PrevText: "- 容器入口（**修复前**）："}},
		{"strikethrough", Item{Path: "docs/runbook.md", Line: 606, Text: "~~Web 侧审计调用无路由：`/audit.v1.` 未被 nginx 代理~~"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Empty(t, Check([]Item{tc.item}, facts), "%s must be exempt", tc.name)
		})
	}

	// Control: the same claim without the qualifier is reported, so the exemption
	// is what suppresses it rather than the rule being inert.
	bare := Item{Path: "docs/ux-review.md", Line: 145, Text: "- 容器入口：`/audit.v1.` 未被 nginx 代理"}
	require.Len(t, Check([]Item{bare}, facts), 1)

	// Review counterexample: a bare "之前" is not a historical qualifier. Only
	// TASK-NNN 之前 (and the explicit words above) exempt a claim.
	unrelated := Item{
		Path: "docs/x.md", Line: 20, Text: "确认 nginx 没有 location `/audit.v1.`",
		PrevText: "在运行 dev-up 之前请检查下列事实：",
	}
	require.Len(t, Check([]Item{unrelated}, facts), 1, "a generic 之前 must not exempt a claim")

	taskRef := Item{
		Path: "docs/x.md", Line: 20, Text: "缺 `/audit.v1.` ⇒ `GetAuthorizationSnapshot` 未被代理",
		PrevText: "- **TASK-249 之前的坑（勿再复现）**：",
	}
	assert.Empty(t, Check([]Item{taskRef}, facts), "TASK-NNN 之前 is a real qualifier")
}

func TestFindingsAreSortedAndDescribed(t *testing.T) {
	facts := testFacts(t)
	findings := Check([]Item{
		{Path: "b.md", Line: 2, Text: "宿主端口段 8082-8087"},
		{Path: "a.md", Line: 9, Text: "`/audit.v1.` 未被 nginx 代理"},
	}, facts)
	require.Len(t, findings, 2)
	assert.Equal(t, "a.md", findings[0].Path)
	assert.Equal(t, "b.md", findings[1].Path)
	assert.True(t, strings.HasPrefix(findings[0].String(), "a.md:9: [absent-prefix] "))
}
