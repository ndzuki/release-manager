<script setup lang="ts">
import { t } from '@/i18n/messages';
import { ref } from 'vue';
import ErrorState from '@/components/common/ErrorState.vue';
import { useArtifactLifecycleStore } from '@/stores/artifactLifecycle';

/*
 * Artifact lifecycle (REQ-069, A7 of the UX plan's missing surfaces).
 *
 * Two destructive maintenance operations that only platform_admin may run:
 *  - retention GC, which runs SYNCHRONOUSLY (budget up to ~1h — the client uses a
 *    per-procedure deadline) and whose successful response can still carry non-fatal
 *    per-phase errors, so "done" and "no problems" are different statements here;
 *  - restoring a bundle the collector archived.
 * A repeated idempotency key inside the 24h window is REFUSED rather than replayed,
 * which is exactly what a retry after a lost response will see.
 */
const store = useArtifactLifecycleStore();

const bundleId = ref('');
const restoreError = ref('');
// One key per GC attempt: the retry button reuses it so the server can recognise the
// attempt, while 执行新一次清理 rotates it.
const gcKey = ref('');
const acknowledged = ref(false);
const runError = ref('');

async function runGc(): Promise<void> {
  if (!acknowledged.value) {
    runError.value = '请先确认已了解清理不可撤销';
    return;
  }
  runError.value = '';
  // A fresh key here means "a new collection"; the retry below keeps this key.
  gcKey.value = crypto.randomUUID();
  await store.clean(gcKey.value);
}

/** Retrying the SAME attempt must reuse its key, or the server cannot recognise it. */
async function retryGc(): Promise<void> {
  if (!gcKey.value) return;
  await store.clean(gcKey.value);
}

async function restore(): Promise<void> {
  const target = bundleId.value.trim();
  if (!target) {
    restoreError.value = t('artifact.bundleIdRequired');
    return;
  }
  restoreError.value = '';
  if (await store.restore(target)) bundleId.value = '';
}

</script>

<template>
  <section class="lifecycle">
    <header class="lifecycle__header">
      <div>
        <p class="eyebrow">{{ t('artifact.eyebrow') }}</p>
        <h1>制品生命周期</h1>
        <p class="lifecycle__subtitle">
          保留期垃圾回收与归档恢复；两者都只对 platform_admin 开放（cleanup/write），维护模式下会被拒绝。
        </p>
      </div>
    </header>

    <p v-if="store.notice" class="lifecycle__notice" role="status" data-testid="lifecycle-notice">{{ store.notice }}</p>

    <section class="lifecycle__panel">
      <h2>执行保留期 GC</h2>
      <p class="lifecycle__warning">
        <strong>不可撤销</strong>：清理会删除超出保留期的 bundle、候选制品与 preflight 记录；计数是<b>行数</b>而非释放的字节。
        调用<b>同步</b>执行（服务端预算可达约 1 小时），因此客户端对本过程使用更长的死线。
      </p>
      <p class="lifecycle__hint">
        同一幂等键在保留窗口内（默认 24 小时，可配置）会被拒绝（<code>cleanup_already_requested</code>）而不重跑；
        「重试」复用同一个键，点「执行 GC」才是新的清理请求。注意：单节点部署可能没有幂等键表，此时只有进程内互斥生效。
      </p>
      <label class="lifecycle__ack">
        <input v-model="acknowledged" type="checkbox" name="acknowledged" />
        我已确认清理会删除超出保留期的制品，且不可撤销
      </label>
      <button type="button" class="danger" :disabled="store.running" data-testid="cleanup-run" @click="runGc">
        {{ store.running ? '执行中…（服务端预算可达约 1 小时）' : '执行 GC' }}
      </button>
      <p v-if="runError" class="lifecycle__error" role="alert" data-testid="cleanup-validation">{{ runError }}</p>

      <ErrorState
        v-if="store.failure"
        title="清理未执行"
        :message="store.failure.message"
        :details="store.failure.details"
        :action-label="store.failure.retryable ? '重试' : ''"
        @action="retryGc"
      />

      <div v-if="store.result" class="lifecycle__result" data-testid="cleanup-result">
        <h3>计数</h3>
        <dl class="lifecycle__counts">
          <dt>删除 bundle</dt><dd data-testid="cleanup-bundles">{{ store.result.deletedBundles }}</dd>
          <dt>删除候选制品</dt><dd>{{ store.result.deletedCandidates }}</dd>
          <dt>删除 preflight</dt><dd>{{ store.result.deletedPreflights }}</dd>
          <dt>跳过 bundle</dt><dd>{{ store.result.skippedBundles }}</dd>
        </dl>
        <div v-if="store.hasProblems" class="lifecycle__problems" role="alert" data-testid="cleanup-errors">
          <h3>非致命错误（{{ store.result.errors.length }}）</h3>
          <p>成功响应里仍可能包含逐阶段错误，需要人工确认：</p>
          <ul>
            <li v-for="(problem, index) in store.result.errors" :key="index"><code>{{ problem }}</code></li>
          </ul>
        </div>
        <p v-else class="lifecycle__hint">本次没有非致命错误。</p>
      </div>
    </section>

    <section class="lifecycle__panel">
      <h2>恢复已归档 Bundle</h2>
      <p class="lifecycle__hint">
        只有「从 <code>validated</code> 归档」的 bundle 能恢复；恢复后回到 <code>validated</code>，不是重新接收。
        该操作是收敛的：重复调用再次成功且不改数据，因此要按<b>存储的原始状态</b>理解 <code>previous_status</code>。
      </p>
      <form @submit.prevent="restore">
        <label>
          {{ t('bundle.detail.bundleId') }}
          <input v-model="bundleId" name="bundleId" placeholder="bundle id" />
        </label>
        <button type="submit" :disabled="store.restoring" data-testid="unarchive-submit">
          {{ store.restoring ? '提交中…' : '恢复' }}
        </button>
        <p v-if="restoreError" class="lifecycle__error" role="alert" data-testid="unarchive-validation">{{ restoreError }}</p>
      </form>

      <ErrorState
        v-if="store.restoreFailure"
        title="恢复未成功"
        :message="store.restoreFailure.message"
        :details="store.restoreFailure.details"
      />

      <p v-if="store.restoreResult" class="lifecycle__restored" role="status" data-testid="unarchive-result">
        已恢复 <code>{{ store.restoreResult.bundleId }}</code>，归档前状态：{{ store.restoreResult.previousStatus }}
      </p>
    </section>
  </section>
</template>

<style scoped>
.lifecycle {
  display: grid;
  gap: var(--space-4);
  padding: var(--space-5);
}

.lifecycle__header h1 {
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

.lifecycle__subtitle {
  margin: var(--space-1) 0 0;
  max-width: 56rem;
  color: var(--color-muted);
}

.lifecycle__panel {
  display: grid;
  gap: var(--space-3);
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
}

.lifecycle__panel h2 {
  margin: 0;
  font-size: var(--font-size-lg);
}

.lifecycle__panel h3 {
  margin: 0;
  font-size: var(--font-size-md);
}

.lifecycle__warning {
  margin: 0;
  padding: var(--space-3);
  border: 1px solid var(--color-danger-border);
  border-radius: var(--radius-md);
  background: var(--color-danger-surface);
  font-size: var(--font-size-sm);
}

.lifecycle__hint {
  margin: 0;
  color: var(--color-muted);
  font-size: var(--font-size-sm);
}

.lifecycle__notice {
  margin: 0;
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-success-border);
  border-radius: var(--radius-lg);
  background: var(--color-success-surface-soft);
  color: var(--color-success-ink);
}

.lifecycle__panel button {
  justify-self: start;
  padding: var(--space-2) var(--space-4);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  cursor: pointer;
}

.lifecycle__panel button.danger {
  border-color: var(--color-danger-border);
  color: var(--color-error);
}

.lifecycle__counts {
  display: grid;
  gap: var(--space-1) var(--space-3);
  grid-template-columns: max-content max-content;
  margin: 0;
}

.lifecycle__counts dt {
  color: var(--color-muted);
}

.lifecycle__problems {
  padding: var(--space-3);
  border: 1px solid var(--color-warning-border);
  border-radius: var(--radius-md);
  background: var(--color-warning-surface);
  color: var(--color-warning-ink-strong);
}

.lifecycle__problems ul {
  margin: var(--space-2) 0 0;
  padding-left: var(--space-4);
}

.lifecycle__ack {
  display: flex;
  gap: var(--space-2);
  align-items: center;
  font-size: var(--font-size-sm);
}

.lifecycle__panel form {
  display: grid;
  gap: var(--space-2);
  justify-items: start;
}

.lifecycle__panel label {
  display: grid;
  gap: var(--space-1);
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-bold);
}

.lifecycle__panel input {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.lifecycle__error {
  margin: 0;
  color: var(--color-error);
  font-size: var(--font-size-sm);
}

.lifecycle__restored {
  margin: 0;
  color: var(--color-success-ink);
}
</style>
