package reqcheck

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTemp creates a temporary file with the given content and returns its path.
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "requirement.md")
	err := os.WriteFile(path, []byte(content), 0o644)
	require.NoError(t, err)
	return path
}

func hasViolation(result *Result, checkID string) bool {
	for _, violation := range result.Violations {
		if violation.CheckID == checkID {
			return true
		}
	}

	return false
}

func TestCheck_AllSectionsPresent(t *testing.T) {
	t.Parallel()

	path := writeTemp(t, `# 某原子需求

## 目标
一句话目标。

## 影响服务
- 某服务

## 输入契约
无。

## 输出契约
无。

## 状态与数据
无。

## 错误模型
无。

## 安全边界
无。

## 验收标准
- [ ] AC-040-01 Given X，When Y，Then Z。

## 非目标
无。

## 回滚方式
无。
`)

	result, err := Check(path)
	require.NoError(t, err)
	assert.Empty(t, result.Violations)

	// All 10 sections should be marked present.
	for _, sec := range RequiredSections {
		assert.True(t, result.Sections[sec], "expected section %q to be present", sec)
	}
}

func TestCheck_MissingSection(t *testing.T) {
	// CHK-01: missing section is a violation.
	t.Parallel()

	path := writeTemp(t, `# 某碎片需求

## 目标
做了个事。

## 影响服务
无
`)

	result, err := Check(path)
	require.NoError(t, err)
	require.NotEmpty(t, result.Violations)

	// At least one violation should be CHK-01.
	hasCHK01 := false
	for _, v := range result.Violations {
		if v.CheckID == "CHK-01" {
			hasCHK01 = true
			assert.Contains(t, v.Message, "missing section")
		}
	}
	assert.True(t, hasCHK01, "expected at least one CHK-01 violation")
}

func TestCheck_SectionWithNA(t *testing.T) {
	// CHK-02: sections marked "不适用" are accepted.
	t.Parallel()

	path := writeTemp(t, `# 某纯配置需求

## 目标
改了一下配置。

## 影响服务
无。

## 输入契约
无。

## 输出契约
无。

## 状态与数据
不适用 — 本需求为纯配置变更，不涉及数据库或状态机。

## 错误模型
无。

## 安全边界
无。

## 验收标准
- [ ] AC-040-02 Given config change，When apply，Then no error。

## 非目标
无。

## 回滚方式
无。
`)

	result, err := Check(path)
	require.NoError(t, err)
	assert.Empty(t, result.Violations)
	assert.True(t, result.NA["状态与数据"])
}

func TestCheck_RejectsNAOutsideSection(t *testing.T) {
	t.Parallel()

	path := writeTemp(t, `# 某需求

不适用 — 不能替代缺失章节。

## 目标
做了个事。

## 影响服务
无。
`)

	result, err := Check(path)
	require.NoError(t, err)
	assert.False(t, result.NA[""], "N/A outside a section must not satisfy a section")
	assert.True(t, hasViolation(result, "CHK-01"))
}

func TestCheck_RejectsNAWithoutReasonAfterPunctuation(t *testing.T) {
	t.Parallel()

	path := writeTemp(t, `# 某需求

## 目标
做了个事。

## 影响服务
无。

## 输入契约
无。

## 输出契约
无。

## 状态与数据
不适用 —

## 错误模型
无。

## 安全边界
无。

## 验收标准
- [ ] AC-040-09 Given X，When Y，Then Z。

## 非目标
无。

## 回滚方式
无。
`)

	result, err := Check(path)
	require.NoError(t, err)
	assert.True(t, hasViolation(result, "CHK-02"))
}

func TestCheck_AcceptanceSectionCanBeNAWithReason(t *testing.T) {
	t.Parallel()

	path := writeTemp(t, `# 某需求

## 目标
做了个事。

## 影响服务
无。

## 输入契约
无。

## 输出契约
无。

## 状态与数据
无。

## 错误模型
无。

## 安全边界
无。

## 验收标准
不适用 — 本需求仅记录已废弃的历史决策，不产生可执行行为。

## 非目标
无。

## 回滚方式
无。
`)

	result, err := Check(path)
	require.NoError(t, err)
	assert.Empty(t, result.Violations)
	assert.True(t, result.NA["验收标准"])
}

func TestCheck_MissingGivenWhenThen(t *testing.T) {
	// CHK-03: AC items must have Given/When/Then.
	t.Parallel()

	path := writeTemp(t, `# 某需求

## 目标
做了个事。

## 影响服务
无。

## 输入契约
无。

## 输出契约
无。

## 状态与数据
无。

## 错误模型
无。

## 安全边界
无。

## 验收标准
- [ ] AC-040-03 应该可以工作。

## 非目标
无。

## 回滚方式
无。
`)

	result, err := Check(path)
	require.NoError(t, err)
	require.NotEmpty(t, result.Violations)

	hasCHK03 := false
	for _, v := range result.Violations {
		if v.CheckID == "CHK-03" {
			hasCHK03 = true
			assert.Contains(t, v.Message, "Given/When/Then")
		}
	}
	assert.True(t, hasCHK03, "expected CHK-03 violation for missing Given/When/Then")
}

func TestCheck_EmptyAcceptanceSection(t *testing.T) {
	// CHK-03: acceptance section present but no AC-XXX-NN items.
	t.Parallel()

	path := writeTemp(t, `# 某需求

## 目标
做了个事。

## 影响服务
无。

## 输入契约
无。

## 输出契约
无。

## 状态与数据
无。

## 错误模型
无。

## 安全边界
无。

## 验收标准
暂无。

## 非目标
无。

## 回滚方式
无。
`)

	result, err := Check(path)
	require.NoError(t, err)
	require.NotEmpty(t, result.Violations)

	hasCHK03 := false
	for _, v := range result.Violations {
		if v.CheckID == "CHK-03" {
			hasCHK03 = true
		}
	}
	assert.True(t, hasCHK03, "expected CHK-03 violation for empty acceptance section")
}

func TestCheck_NARequiresReason(t *testing.T) {
	t.Parallel()

	path := writeTemp(t, `# 某需求

## 目标
做了个事。

## 影响服务
无。

## 输入契约
无。

## 输出契约
无。

## 状态与数据
不适用

## 错误模型
无。

## 安全边界
无。

## 验收标准
- [ ] AC-040-05 Given X，When Y，Then Z。

## 非目标
无。

## 回滚方式
无。
`)

	result, err := Check(path)
	require.NoError(t, err)
	assert.True(t, hasViolation(result, "CHK-02"))
}

func TestCheck_AcMustBeChecklistItem(t *testing.T) {
	t.Parallel()

	path := writeTemp(t, `# 某需求

## 目标
做了个事。

## 影响服务
无。

## 输入契约
无。

## 输出契约
无。

## 状态与数据
无。

## 错误模型
无。

## 安全边界
无。

## 验收标准
正文示例：AC-040-06 Given X，When Y，Then Z。

## 非目标
无。

## 回滚方式
无。
`)

	result, err := Check(path)
	require.NoError(t, err)
	assert.True(t, hasViolation(result, "CHK-03"))
}

func TestCheck_RejectsMalformedAcceptanceID(t *testing.T) {
	t.Parallel()

	path := writeTemp(t, `# 某需求

## 目标
做了个事。

## 影响服务
无。

## 输入契约
无。

## 输出契约
无。

## 状态与数据
无。

## 错误模型
无。

## 安全边界
无。

## 验收标准
- [ ] AC-40-1 Given X，When Y，Then Z。

## 非目标
无。

## 回滚方式
无。
`)

	result, err := Check(path)
	require.NoError(t, err)
	assert.True(t, hasViolation(result, "CHK-03"))
}

func TestCheck_RejectsGivenWhenThenOutOfOrder(t *testing.T) {
	t.Parallel()

	path := writeTemp(t, `# 某需求

## 目标
做了个事。

## 影响服务
无。

## 输入契约
无。

## 输出契约
无。

## 状态与数据
无。

## 错误模型
无。

## 安全边界
无。

## 验收标准
- [ ] AC-040-10 Then Z，When Y，Given X。

## 非目标
无。

## 回滚方式
无。
`)

	result, err := Check(path)
	require.NoError(t, err)
	assert.True(t, hasViolation(result, "CHK-03"))
}

func TestCheck_RejectsManualImplementationInterpretation(t *testing.T) {
	t.Parallel()

	path := writeTemp(t, `# 某需求

## 目标
做了个事。

## 影响服务
无。

## 输入契约
无。

## 输出契约
无。

## 状态与数据
无。

## 错误模型
无。

## 安全边界
无。

## 验收标准
- [ ] AC-040-07 Given X，When Y，Then 需要人工检查内部实现。

## 非目标
无。

## 回滚方式
无。
`)

	result, err := Check(path)
	require.NoError(t, err)
	assert.True(t, hasViolation(result, "CHK-04"))
}

func TestCheck_AcCanExplicitlyRejectManualInterpretation(t *testing.T) {
	t.Parallel()

	path := writeTemp(t, `# 某需求

## 目标
做了个事。

## 影响服务
无。

## 输入契约
无。

## 输出契约
无。

## 状态与数据
无。

## 错误模型
无。

## 安全边界
无。

## 验收标准
- [ ] AC-040-08 Given X，When Y，Then 结果不依赖人工解释内部实现。

## 非目标
无。

## 回滚方式
无。
`)

	result, err := Check(path)
	require.NoError(t, err)
	assert.Empty(t, result.Violations)
}

func TestCheck_FileNotFound(t *testing.T) {
	t.Parallel()

	_, err := Check("/nonexistent/file.md")
	assert.Error(t, err)
}

func TestCheck_MixedCaseGivenWhenThen(t *testing.T) {
	// Given/When/Then matching is case-insensitive.
	t.Parallel()

	path := writeTemp(t, `# 某需求

## 目标
做了个事。

## 影响服务
无。

## 输入契约
无。

## 输出契约
无。

## 状态与数据
无。

## 错误模型
无。

## 安全边界
无。

## 验收标准
- [ ] AC-040-04 given some state，WHEN action taken，THEN result expected。

## 非目标
无。

## 回滚方式
无。
`)

	result, err := Check(path)
	require.NoError(t, err)
	assert.Empty(t, result.Violations)
}

func TestCheck_MultipleViolations(t *testing.T) {
	// TASK-159: a lite document is judged against the lite core, while a
	// `tier: full` document must still carry all ten sections.
	t.Parallel()

	lite := writeTemp(t, `# 某需求

## 目标
做了个事。
`)
	result, err := Check(lite)
	require.NoError(t, err)
	require.False(t, result.Skipped)
	assert.GreaterOrEqual(t, len(result.Violations), 2, "the lite core sections are still required")
	assert.Less(t, len(result.Violations), 9,
		"a lite document must not be judged against the full template")

	full := writeTemp(t, `---
tier: full
---

# 某需求

## 目标
做了个事。
`)
	fullResult, err := Check(full)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(fullResult.Violations), 9,
		"a tier: full document must still carry every section")
}

// TASK-159 negative control: a roadmap/domain index or an archived record is
// not an implementable REQ and must be skipped, not failed. Removing the
// delivery_scope check makes this test fail.
func TestCheck_SkipsIndexAndArchivedScopes(t *testing.T) {
	t.Parallel()

	for _, scope := range []string{"index", "archived"} {
		path := writeTemp(t, "---\ndelivery_scope: "+scope+"\n---\n\n# 索引\n\n没有验收标准，也没有目标小节。\n")
		result, err := Check(path)
		require.NoError(t, err)
		assert.True(t, result.Skipped, "delivery_scope=%s must be skipped", scope)
		assert.Empty(t, result.Violations, "a skipped document must report no violations")
	}
}

// TASK-159: a delivered REQ's acceptance text is frozen history -- its delivery
// evidence was recorded against that exact wording -- so the Given/When/Then
// style rule must not force a rewrite of a delivered requirement. It must still
// apply to a REQ that is not yet delivered. Removing the status check from
// validateAcceptance makes this test fail.
func TestCheck_DeliveredRequirementIsNotForcedToRewriteItsCriteria(t *testing.T) {
	t.Parallel()

	body := `## 目标
无。

## 验收标准
- [ ] AC-906-01 多镜像 Bundle 正确绑定到 values path。

## 非目标
无。
`
	delivered := writeTemp(t, "---\nstatus: delivered\n---\n\n"+body)
	result, err := Check(delivered)
	require.NoError(t, err)
	assert.Empty(t, result.Violations, "a delivered REQ must not be flagged for AC style")

	active := writeTemp(t, "---\nstatus: accepted\n---\n\n"+body)
	activeResult, err := Check(active)
	require.NoError(t, err)
	require.NotEmpty(t, activeResult.Violations, "a non-delivered REQ must still be flagged")
	assert.Contains(t, activeResult.Violations[0].Message, "lacks Given/When/Then")
}
