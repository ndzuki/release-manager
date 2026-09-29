<script setup lang="ts">
import { t } from '@/i18n/messages';
import { onMounted, ref } from 'vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import { BUNDLE_STATUSES } from '@/connect/bundle-api';
import { useBundlesStore } from '@/stores/bundles';

/*
 * Release bundle catalogue (A8 of the UX plan's missing surfaces).
 *
 * Bundles used to be a hidden dropdown in the operation form; this page makes the
 * content-addressable input visible: digests, chart reference, image bindings and —
 * in the detail panel — the git/pipeline provenance and the signature/SBOM/
 * provenance evidence references.
 */
const store = useBundlesStore();

const definitionFilter = ref('');
const chartFilter = ref('');
const statusFilter = ref('');

onMounted(() => {
  void store.load();
});

function applyFilters(): void {
  // A cursor is bound to the filter that produced it, so this always restarts.
  void store.load({
    releaseDefinitionId: definitionFilter.value.trim(),
    chartNameFilter: chartFilter.value.trim(),
    status: statusFilter.value === '' ? null : Number(statusFilter.value),
  });
}

function formatTimestamp(value: string | null): string {
  return value ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) : '—';
}
</script>

<template>
  <section class="bundles">
    <header class="bundles__header">
      <div>
        <p class="eyebrow">{{ t('bundle.eyebrow') }}</p>
        <h1>发布 Bundle</h1>
        <p class="bundles__subtitle">
          Bundle 是不可变输入：内容摘要、Chart 引用与镜像绑定，以及签名/SBOM/来源证明的引用。
        </p>
      </div>
      <span class="bundles__total" data-testid="bundles-total">共 {{ store.totalSize }} 个</span>
    </header>

    <form class="bundles__filters" @submit.prevent="applyFilters">
      <label>
        {{ t('definitions.releaseDefinitionIdRequired') }}
        <input v-model="definitionFilter" name="releaseDefinitionId" placeholder="definition id" required />
      </label>
      <label>
        {{ t('definitions.chartNameOptional') }}
        <input v-model="chartFilter" name="chartNameFilter" placeholder="chart name" />
      </label>
      <label>
        状态
        <select v-model="statusFilter" name="status">
          <option value="">全部</option>
          <option v-for="entry in BUNDLE_STATUSES" :key="entry.value" :value="String(entry.value)">{{ entry.label }}</option>
        </select>
      </label>
      <button type="submit" :disabled="store.loading">{{ store.loading ? '查询中…' : '查询' }}</button>
    </form>

    <ErrorState
      v-if="store.failure && !store.needsDefinition"
      title="加载未成功"
      :message="store.failure.message"
      :details="store.failure.details"
      action-label="回到第一页"
      @action="applyFilters"
    />

    <p v-if="store.needsDefinition" class="bundles__prompt" role="status" data-testid="bundles-needs-definition">
      请先填写 Release Definition ID：服务端把它同时作为<b>查询范围</b>与<b>授权对象</b>，缺少它会被拒绝
      （不是「没有数据」），因此页面在填写前不会发起请求。
    </p>

    <LoadingState v-else-if="store.loading && store.bundles.length === 0" message="正在读取 Bundle…" />
    <EmptyState
      v-else-if="store.isEmpty && !store.needsDefinition"
      title="没有 Bundle"
      message="当前过滤条件下没有 Bundle；清空过滤可查看全部（注意：无权限时服务端会返回拒绝而不是空列表）。"
    />

    <table v-else-if="store.bundles.length > 0" class="bundles__table">
      <thead>
        <tr>
          <th scope="col">名称</th>
          <th scope="col">状态</th>
          <th scope="col">摘要</th>
          <th scope="col">Chart</th>
          <th scope="col">镜像绑定</th>
          <th scope="col">接收时间</th>
          <th scope="col">操作</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="bundle in store.bundles" :key="bundle.id" :data-testid="`bundle-row-${bundle.id}`">
          <td>
            <strong>{{ bundle.name }}</strong>
            <br />
            <small>{{ bundle.id }}</small>
          </td>
          <td><span class="bundles__status">{{ bundle.statusLabel }}</span></td>
          <td><code class="bundles__digest">{{ bundle.digest }}</code></td>
          <td>
            {{ bundle.chartRef }}@{{ bundle.chartVersion }}
            <br />
            <small>{{ bundle.chartDigest }}</small>
          </td>
          <td>{{ bundle.images.length }} 个</td>
          <td>{{ formatTimestamp(bundle.createdAt) }}</td>
          <td>
            <button type="button" :data-testid="`bundle-detail-${bundle.id}`" @click="store.openDetail(bundle)">明细</button>
          </td>
        </tr>
      </tbody>
    </table>

    <div v-if="store.hasMore" class="bundles__more">
      <button type="button" :disabled="store.appending" data-testid="bundles-load-more" @click="store.appendNextPage">
        {{ store.appending ? '加载中…' : '加载更多' }}
      </button>
    </div>

    <p v-if="store.appending && store.bundles.length > 0" class="bundles__hint">正在追加下一页…</p>

    <section v-if="store.detail || store.detailLoading || store.detailFailure" class="bundles__detail" data-testid="bundle-detail-panel">
      <h2>Bundle 明细</h2>
      <LoadingState v-if="store.detailLoading" message="正在读取明细…" />
      <ErrorState v-else-if="store.detailFailure" title="明细加载失败" :message="store.detailFailure.message" :details="store.detailFailure.details" />
      <template v-else-if="store.detail">
        <dl class="bundles__facts">
          <dt>名称</dt><dd data-testid="detail-name">{{ store.detail.name }}</dd>
          <dt>摘要</dt><dd><code>{{ store.detail.digest }}</code></dd>
          <dt>状态</dt><dd>{{ store.detail.statusLabel }}</dd>
          <dt>{{ t('bundle.detail.gitCommit') }}</dt><dd data-testid="detail-git">{{ store.detail.gitCommit || '—' }}</dd>
          <dt>{{ t('bundle.detail.pipeline') }}</dt><dd>{{ store.detail.pipelineId || '—' }}</dd>
        </dl>

        <h3>证据引用</h3>
        <dl class="bundles__facts" data-testid="detail-evidence">
          <dt>签名</dt><dd>{{ store.detail.signatureDigest || '—' }} <small>{{ store.detail.signatureRef }}</small></dd>
          <dt>{{ t('bundle.detail.sbom') }}</dt><dd>{{ store.detail.sbomDigest || '—' }} <small>{{ store.detail.sbomRef }}</small></dd>
          <dt>来源证明</dt><dd>{{ store.detail.provenanceDigest || '—' }} <small>{{ store.detail.provenanceRef }}</small></dd>
        </dl>

        <h3>镜像绑定（{{ store.detail.images.length }}）</h3>
        <table class="bundles__images">
          <thead>
            <tr><th scope="col">引用</th><th scope="col">摘要</th><th scope="col">Values 路径</th></tr>
          </thead>
          <tbody>
            <tr v-for="image in store.detail.images" :key="`${image.valuesPath}-${image.ref}`">
              <td><code>{{ image.ref }}</code></td>
              <td><code>{{ image.digest }}</code></td>
              <td>{{ image.valuesPath }}</td>
            </tr>
          </tbody>
        </table>

        <button type="button" @click="store.closeDetail()">关闭明细</button>
      </template>
    </section>
  </section>
</template>

<style scoped>
.bundles {
  display: grid;
  gap: var(--space-4);
  padding: var(--space-5);
}

.bundles__header {
  display: flex;
  gap: var(--space-3);
  align-items: flex-start;
  justify-content: space-between;
}

.bundles__header h1 {
  margin: 0;
  font-size: var(--font-size-xl);
}

.eyebrow {
  margin: 0;
  color: var(--color-primary);
  font-size: var(--font-size-xs);
  font-weight: 800;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.bundles__prompt {
  margin: 0;
  padding: var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-surface-muted);
  color: var(--color-text);
}

.bundles__subtitle {
  margin: var(--space-1) 0 0;
  max-width: 50rem;
  color: var(--color-muted);
}

.bundles__total {
  color: var(--color-muted);
  font-size: var(--font-size-sm);
}

.bundles__filters {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-3);
  align-items: flex-end;
  padding: var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
}

.bundles__filters label {
  display: grid;
  gap: var(--space-1);
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-bold);
}

.bundles__filters input,
.bundles__filters select {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.bundles__table,
.bundles__images {
  width: 100%;
  border-collapse: collapse;
  background: var(--color-surface);
}

.bundles__table th,
.bundles__table td,
.bundles__images th,
.bundles__images td {
  padding: var(--space-2) var(--space-3);
  border-bottom: 1px solid var(--color-border);
  text-align: left;
}

.bundles__table small,
.bundles__hint {
  color: var(--color-muted);
}

.bundles__digest {
  word-break: break-all;
}

.bundles__status {
  padding: 0.1rem var(--space-2);
  border-radius: var(--radius-xl);
  background: var(--color-surface-muted);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-bold);
}

.bundles__table button,
.bundles__filters button,
.bundles__more button,
.bundles__detail button {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  cursor: pointer;
}

.bundles__more {
  display: flex;
  justify-content: center;
}

.bundles__detail {
  display: grid;
  gap: var(--space-3);
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
}

.bundles__detail h2 {
  margin: 0;
  font-size: var(--font-size-lg);
}

.bundles__detail h3 {
  margin: var(--space-2) 0 0;
  font-size: var(--font-size-md);
}

.bundles__facts {
  display: grid;
  gap: var(--space-1) var(--space-3);
  grid-template-columns: max-content 1fr;
  margin: 0;
}

.bundles__facts dt {
  color: var(--color-muted);
}
</style>
