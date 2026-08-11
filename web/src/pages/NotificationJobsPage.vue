<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue';
import { storeToRefs } from 'pinia';
import Drawer from '@/components/common/Drawer.vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import Toast from '@/components/common/Toast.vue';
import { NotificationChannel, NotificationStatus } from '@/gen/notifier/v1/notifier_pb';
import { useNotificationJobsStore } from '@/stores/notificationJobs';

const store = useNotificationJobsStore();
const {
  jobs,
  filters,
  totalCount,
  detail,
  attempts,
  drawerOpen,
  listLoading,
  detailLoading,
  retryLoading,
  listError,
  detailError,
  notice,
  hasFilters,
  cursorHistory,
  nextCursor,
} = storeToRefs(store);
const recipientInput = ref(filters.value.recipient);
const retryReason = ref('');
const originalJobID = ref('');
const expiredRelated = ref(new Set<string>());
let recipientTimer: ReturnType<typeof setTimeout> | undefined;

const retryReasonLength = computed(() => Array.from(retryReason.value.trim()).length);
const retryReasonError = computed(() => retryReasonLength.value > 0 && (retryReasonLength.value < 10 || retryReasonLength.value > 500));
const statuses = [
  NotificationStatus.PENDING,
  NotificationStatus.SENDING,
  NotificationStatus.DELIVERED,
  NotificationStatus.FAILED,
  NotificationStatus.RETRY_WAIT,
  NotificationStatus.DEAD_LETTER,
];
const channels = [NotificationChannel.WEBHOOK, NotificationChannel.EMAIL, NotificationChannel.SLACK];
const statusName = (value: NotificationStatus) => NotificationStatus[value].toLowerCase().replace('_', ' ');
const channelName = (value: NotificationChannel) => NotificationChannel[value].toLowerCase();
const formatTime = (timestamp?: { seconds: bigint }) =>
  timestamp ? new Date(Number(timestamp.seconds) * 1000).toLocaleString() : '—';
const isRetryWait = (job: { status: NotificationStatus; nextRetryAt?: { seconds: bigint } }) =>
  job.status === NotificationStatus.FAILED &&
  Boolean(job.nextRetryAt && Number(job.nextRetryAt.seconds) * 1000 > Date.now());

function changeRecipient(): void {
  if (recipientTimer) clearTimeout(recipientTimer);
  recipientTimer = setTimeout(() => {
    filters.value.recipient = recipientInput.value;
    store.resetPagination();
    void store.loadJobs('');
  }, 300);
}

function toggleStatus(status: NotificationStatus, event: Event): void {
  store.setStatusFilter(status, (event.target as HTMLInputElement).checked);
  void store.loadJobs('');
}

function toggleChannel(channel: NotificationChannel, event: Event): void {
  store.setChannelFilter(channel, (event.target as HTMLInputElement).checked);
  void store.loadJobs('');
}

function clearFilters(): void {
  store.resetFilters();
  recipientInput.value = '';
  void store.loadJobs('');
}

async function openJob(jobID: string): Promise<void> {
  originalJobID.value = jobID;
  expiredRelated.value = new Set<string>();
  await store.openDrawer(jobID);
  await detectExpiredRelatedJobs();
}

async function detectExpiredRelatedJobs(): Promise<void> {
  const current = detail.value;
  if (!current) return;
  const relatedJobIDs = [current.replayOf, ...current.replayedBy].filter(Boolean);
  if (!relatedJobIDs.length) return;

  const expired = new Set<string>();
  await Promise.all(
    relatedJobIDs.map(async (relatedJobID) => {
      if (!(await store.jobExists(relatedJobID))) expired.add(relatedJobID);
    }),
  );
  expiredRelated.value = expired;
}

async function replay(): Promise<void> {
  if (retryReasonError.value || retryReasonLength.value < 10) return;
  if (await store.retryJob(retryReason.value.trim())) retryReason.value = '';
}

async function openRelated(jobID: string): Promise<void> {
  if (expiredRelated.value.has(jobID)) return;
  if (!originalJobID.value) originalJobID.value = detail.value?.jobId ?? '';
  const result = await store.loadJob(jobID);
  if (result === 'not-found') {
    expiredRelated.value = new Set(expiredRelated.value).add(jobID);
    notice.value = { kind: 'warning', message: '目标任务已过期' };
  } else if (result === 'failed') {
    notice.value = { kind: 'error', message: '获取详情失败' };
  }
}

async function returnToOriginal(): Promise<void> {
  if (originalJobID.value && detail.value?.jobId !== originalJobID.value) {
    const result = await store.loadJob(originalJobID.value);
    if (result !== 'loaded') notice.value = { kind: 'warning', message: '原任务已过期' };
  }
}

function closeDrawer(): void {
  originalJobID.value = '';
  expiredRelated.value = new Set<string>();
  retryReason.value = '';
  store.closeDrawer();
}

onMounted(() => void store.loadJobs());
onBeforeUnmount(() => {
  if (recipientTimer) clearTimeout(recipientTimer);
});
</script>

<template>
  <section class="jobs-page">
    <header class="jobs-page__heading">
      <div><p>Operations</p><h1>Notification jobs</h1></div>
      <div class="heading-actions"><strong>{{ totalCount }} total</strong><button type="button" :disabled="listLoading" @click="store.loadJobs()">刷新</button></div>
    </header>

    <div class="filters" aria-label="Notification job filters">
      <fieldset>
        <legend>Status</legend>
        <label v-for="status in statuses" :key="status">
          <input type="checkbox" :checked="filters.status.includes(status)" @change="toggleStatus(status, $event)">
          {{ statusName(status) }}
        </label>
      </fieldset>
      <fieldset>
        <legend>Channel</legend>
        <label v-for="channel in channels" :key="channel">
          <input type="checkbox" :checked="filters.channel.includes(channel)" @change="toggleChannel(channel, $event)">
          {{ channelName(channel) }}
        </label>
      </fieldset>
      <label class="recipient">Recipient<input v-model="recipientInput" type="search" placeholder="Search recipient" @input="changeRecipient"></label>
      <button v-if="hasFilters" type="button" @click="clearFilters">Reset filters</button>
    </div>

    <div v-if="listError" class="banner" role="alert">
      <span>{{ listError }}</span><button type="button" @click="store.loadJobs()">Retry</button>
    </div>
    <div v-if="listLoading" class="skeleton" aria-label="Loading notification jobs"><span v-for="n in 3" :key="n" /></div>
    <EmptyState
      v-else-if="!jobs.length"
      :title="hasFilters ? '当前筛选条件下无匹配结果' : '暂无通知任务'"
      :message="hasFilters ? '清除筛选条件后查看全部任务。' : '通知任务创建后将显示在这里。'"
      :action-label="hasFilters ? '清除筛选' : ''"
      @action="clearFilters"
    />
    <div v-else class="table-wrap">
      <table>
        <thead><tr><th>Status</th><th>Channel</th><th>Recipient</th><th>Operation</th><th>Attempts</th><th>Replay chain</th><th>Created</th></tr></thead>
        <tbody>
          <tr v-for="job in jobs" :key="job.jobId" tabindex="0" @click="openJob(job.jobId)" @keydown.enter="openJob(job.jobId)">
            <td><span class="badge" :class="[`badge--${statusName(job.status).replace(' ', '-')}`, { 'badge--retry': isRetryWait(job) }]">{{ isRetryWait(job) ? 'retry wait' : statusName(job.status) }}</span></td>
            <td>{{ channelName(job.channel) }}</td><td>{{ job.displayRecipient }}</td><td>{{ job.operationId }}</td><td>{{ job.attempts }}</td>
            <td><span v-if="job.replayOf">重放自 {{ job.replayOf }}</span><span v-else-if="job.replayedBy.length">已被重放 {{ job.replayedBy.length }} 次</span><span v-else>—</span></td>
            <td>{{ formatTime(job.createdAt) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
    <footer class="pagination"><span>{{ totalCount }} jobs</span><div><button type="button" :disabled="!cursorHistory.length || listLoading" @click="store.previousPage">Previous</button><button type="button" :disabled="!nextCursor || listLoading" @click="store.nextPage">Next</button></div></footer>

    <Drawer :open="drawerOpen" title="Notification job details" @close="closeDrawer">
      <div v-if="detailLoading" class="detail-skeleton" aria-label="Loading notification job details"><span v-for="n in 7" :key="n" /></div>
      <ErrorState v-else-if="!detail" title="Unable to load job" :message="detailError" action-label="关闭" @action="closeDrawer" />
      <div v-else class="detail">
        <button v-if="originalJobID && originalJobID !== detail.jobId" type="button" @click="returnToOriginal">← 返回原任务</button>
        <dl>
          <div><dt>Job ID</dt><dd>{{ detail.jobId }}</dd></div><div><dt>Status</dt><dd>{{ statusName(detail.status) }}</dd></div>
          <div><dt>Recipient</dt><dd>{{ detail.displayRecipient }}</dd></div><div><dt>Channel</dt><dd>{{ channelName(detail.channel) }}</dd></div>
          <div><dt>Operation</dt><dd>{{ detail.operationId }}</dd></div><div><dt>Version</dt><dd>{{ detail.version }}</dd></div>
          <div><dt>Created</dt><dd>{{ formatTime(detail.createdAt) }}</dd></div><div><dt>Updated</dt><dd>{{ formatTime(detail.updatedAt) }}</dd></div>
          <div><dt>Next retry</dt><dd>{{ formatTime(detail.nextRetryAt) }}</dd></div><div v-if="detail.retryReason"><dt>Retry reason</dt><dd>{{ detail.retryReason }}</dd></div>
        </dl>
        <section v-if="detail.replayOf || detail.replayedBy.length" class="replay-chain">
          <h3>Replay chain</h3>
          <p v-if="detail.replayOf">Replay of
            <span v-if="expiredRelated.has(detail.replayOf)">{{ detail.replayOf }}（已过期）</span>
            <button v-else type="button" @click="openRelated(detail.replayOf)">{{ detail.replayOf }}</button>
          </p>
          <p v-for="child in detail.replayedBy" :key="child">Replayed by
            <span v-if="expiredRelated.has(child)">{{ child }}（已过期）</span>
            <button v-else type="button" @click="openRelated(child)">{{ child }}</button>
          </p>
        </section>
        <section><h3>Attempts</h3><ol class="timeline"><li v-for="attempt in attempts" :key="attempt.attemptNumber"><strong>Attempt {{ attempt.attemptNumber }} · {{ attempt.status }}</strong><span>{{ formatTime(attempt.timestamp) }} · {{ attempt.durationMs }}ms · {{ attempt.httpStatusCode ? `HTTP ${attempt.httpStatusCode}` : '网络错误' }}</span><p v-if="attempt.errorMessage">{{ attempt.errorMessage }}</p></li></ol><EmptyState v-if="!attempts.length" title="No delivery attempts" message="This job has not been attempted yet." /></section>
        <form v-if="detail.status === NotificationStatus.DEAD_LETTER" class="replay" @submit.prevent="replay"><h3>Replay dead-letter job</h3><label>Reason<textarea v-model="retryReason" minlength="10" maxlength="500" required /></label><small :class="{ invalid: retryReasonError }">{{ retryReasonLength }}/500 · minimum 10 characters</small><button type="submit" :disabled="retryLoading || retryReasonError || retryReasonLength < 10">{{ retryLoading ? '重放中...' : '确认重放' }}</button></form>
      </div>
    </Drawer>
    <Toast :show="Boolean(notice)" :kind="notice?.kind ?? 'success'" :message="notice?.message ?? ''" @close="notice = null" />
  </section>
</template>

<style scoped>
.jobs-page { display: grid; gap: 1.25rem; } .jobs-page__heading,.heading-actions { display:flex; justify-content:space-between; align-items:center; gap:.75rem; } .jobs-page__heading p,.jobs-page__heading h1 { margin:0; } .jobs-page__heading p { color:var(--color-muted,#64748b); font-size:.75rem; font-weight:700; text-transform:uppercase; letter-spacing:.08em; }
.filters { display:flex; flex-wrap:wrap; align-items:end; gap:1rem; padding:1rem; border:1px solid var(--color-border,#e2e8f0); background:var(--color-surface,#fff); } fieldset { display:flex; flex-wrap:wrap; gap:.65rem; margin:0; border:0; padding:0; } legend,.recipient { font-size:.75rem; font-weight:700; } fieldset label { font-size:.85rem; } .recipient { display:grid; gap:.35rem; } input[type=search],textarea { padding:.5rem .65rem; border:1px solid var(--color-border,#cbd5e1); border-radius:.375rem; }
.banner { display:flex; justify-content:space-between; padding:.75rem 1rem; background:#fef2f2; color:#991b1b; } .skeleton,.detail-skeleton { display:grid; gap:.65rem; } .skeleton span,.detail-skeleton span { height:3rem; border-radius:.375rem; background:#e2e8f0; animation:pulse 1s ease-in-out infinite alternate; }
.table-wrap { overflow:auto; border:1px solid var(--color-border,#e2e8f0); } table { width:100%; border-collapse:collapse; background:var(--color-surface,#fff); } th,td { padding:.8rem 1rem; border-bottom:1px solid var(--color-border,#e2e8f0); text-align:left; } th { color:var(--color-muted,#64748b); font-size:.75rem; text-transform:uppercase; } tbody tr { cursor:pointer; } tbody tr:hover,tbody tr:focus { background:#f1f5f9; outline:2px solid var(--color-primary,#2563eb); outline-offset:-2px; } .badge { display:inline-flex; padding:.2rem .5rem; border-radius:999px; background:#dbeafe; color:#1d4ed8; font-size:.75rem; font-weight:700; } .badge--sending { background:#e2e8f0; color:#475569; } .badge--delivered { background:#dcfce7; color:#166534; } .badge--failed { background:#ffedd5; color:#9a3412; } .badge--dead-letter { background:#fee2e2; color:#991b1b; } .badge--retry { background:#fef3c7; color:#a16207; }
.pagination { display:flex; justify-content:space-between; } button { padding:.45rem .75rem; border:1px solid var(--color-border,#cbd5e1); border-radius:.375rem; background:var(--color-surface,#fff); cursor:pointer; } button:disabled { opacity:.5; cursor:not-allowed; } .pagination div { display:flex; gap:.5rem; }
.detail { display:grid; gap:1.5rem; } dl { display:grid; gap:.75rem; margin:0; } dl div { display:grid; grid-template-columns:7rem 1fr; } dt { color:var(--color-muted,#64748b); } dd { margin:0; overflow-wrap:anywhere; } .replay-chain p { overflow-wrap:anywhere; } .timeline { display:grid; gap:1rem; padding-left:1.5rem; } .timeline li { display:grid; gap:.25rem; } .timeline span { color:var(--color-muted,#64748b); font-size:.8rem; } .timeline p { margin:0; color:#991b1b; } .replay { display:grid; gap:.6rem; padding-top:1rem; border-top:1px solid var(--color-border,#e2e8f0); } .replay label { display:grid; gap:.35rem; } textarea { min-height:6rem; resize:vertical; } small.invalid { color:#b91c1c; }
@keyframes pulse { to { opacity:.45; } } @media(max-width:44rem){.jobs-page__heading,.pagination{align-items:start;flex-direction:column;gap:.75rem}.filters{align-items:stretch;flex-direction:column}}
</style>
