import { defineStore } from 'pinia';
import { Code, ConnectError } from '@connectrpc/connect';
import { computed, ref, shallowRef } from 'vue';
import { notificationJobsClient } from '@/connect/client';
import {
  NotificationChannel,
  NotificationStatus,
  type Attempt,
  type NotificationJobDetail,
  type NotificationJobSummary,
} from '@/gen/notifier/v1/notifier_pb';

export interface NotificationJobFilters {
  status: NotificationStatus[];
  channel: NotificationChannel[];
  recipient: string;
}

export type NotificationJobsNotice = { kind: 'success' | 'error' | 'warning'; message: string } | null;

export type NotificationJobLoadResult = 'loaded' | 'not-found' | 'failed';

const defaultFilters = (): NotificationJobFilters => ({ status: [], channel: [], recipient: '' });
const listErrorMessage = '加载失败，请检查网络连接后重试';

export const useNotificationJobsStore = defineStore('notificationJobs', () => {
  const jobs = shallowRef<NotificationJobSummary[]>([]);
  const filters = ref<NotificationJobFilters>(defaultFilters());
  const cursor = ref('');
  const nextCursor = ref('');
  const cursorHistory = ref<string[]>([]);
  const totalCount = ref(0);
  const detail = shallowRef<NotificationJobDetail | null>(null);
  const attempts = shallowRef<Attempt[]>([]);
  const drawerOpen = ref(false);
  const listLoading = ref(false);
  const detailLoading = ref(false);
  const retryLoading = ref(false);
  const listError = ref('');
  const detailError = ref('');
  const notice = shallowRef<NotificationJobsNotice>(null);
  const hasFilters = computed(
    () => filters.value.status.length > 0 || filters.value.channel.length > 0 || filters.value.recipient.trim() !== '',
  );

  async function loadJobs(pageToken = cursor.value): Promise<void> {
    listLoading.value = true;
    listError.value = '';
    try {
      const response = await notificationJobsClient.listNotificationJobs({
        pageToken,
        pageSize: 20,
        status: filters.value.status,
        channel: filters.value.channel,
        recipient: filters.value.recipient.trim(),
      });
      jobs.value = response.jobs;
      cursor.value = pageToken;
      nextCursor.value = response.nextPageToken;
      totalCount.value = response.totalCount;
    } catch {
      listError.value = listErrorMessage;
    } finally {
      listLoading.value = false;
    }
  }

  async function loadJob(jobId: string): Promise<NotificationJobLoadResult> {
    detailLoading.value = true;
    detailError.value = '';
    try {
      const response = await notificationJobsClient.getNotificationJob({ jobId });
      if (!response.job) {
        detailError.value = '任务不存在或已过期';
        return 'not-found';
      }
      detail.value = response.job;
      attempts.value = response.attempts;
      return 'loaded';
    } catch (cause) {
      const connectError = ConnectError.from(cause);
      detailError.value = connectError.code === Code.NotFound ? '任务不存在或已过期' : '获取详情失败';
      return connectError.code === Code.NotFound ? 'not-found' : 'failed';
    } finally {
      detailLoading.value = false;
    }
  }

  async function jobExists(jobId: string): Promise<boolean> {
    try {
      const response = await notificationJobsClient.getNotificationJob({ jobId });
      return Boolean(response.job);
    } catch (cause) {
      return ConnectError.from(cause).code !== Code.NotFound;
    }
  }

  async function getJob(jobId: string): Promise<void> {
    const result = await loadJob(jobId);
    if (result !== 'loaded') {
      notice.value = { kind: 'error', message: detailError.value };
    }
  }

  async function retryJob(reason: string): Promise<boolean> {
    const current = detail.value;
    if (!current || retryLoading.value) return false;
    retryLoading.value = true;
    try {
      const response = await notificationJobsClient.retryNotificationJob({
        jobId: current.jobId,
        version: current.version,
        retryReason: reason,
      });
      if (!response.job) return false;
		const replayedJob = response.job;
		detail.value = replayedJob;
		attempts.value = [];
		jobs.value = jobs.value.map((job) =>
			job.jobId === current.jobId
				? { ...job, replayedBy: [...job.replayedBy, replayedJob.jobId], version: job.version + 1n }
				: job,
		);
		notice.value = { kind: 'success', message: '重放已提交' };
      return true;
    } catch (cause) {
      const connectError = ConnectError.from(cause);
      if (connectError.code === Code.Aborted || connectError.rawMessage.includes('optimistic_lock_conflict')) {
        const result = await loadJob(current.jobId);
        if (result === 'loaded' && detail.value) syncListJob(detail.value);
        notice.value = { kind: 'warning', message: '该任务已被更新，请确认后重试' };
      } else if (connectError.code === Code.FailedPrecondition || connectError.rawMessage.includes('retry_not_allowed')) {
        const result = await loadJob(current.jobId);
        if (result === 'loaded' && detail.value) syncListJob(detail.value);
        notice.value = { kind: 'error', message: '当前任务状态不允许重放' };
      } else {
        notice.value = { kind: 'error', message: '重放请求失败，请稍后重试' };
      }
      return false;
    } finally {
      retryLoading.value = false;
    }
  }

  function syncListJob(updated: NotificationJobDetail): void {
    jobs.value = jobs.value.map((job) =>
      job.jobId === updated.jobId
        ? {
            ...job,
            status: updated.status,
            attempts: updated.attempts,
            lastError: updated.lastError,
            version: updated.version,
            replayedBy: [...updated.replayedBy],
            replayOf: updated.replayOf,
            nextRetryAt: updated.nextRetryAt,
          }
        : job,
    );
  }

  function setStatusFilter(status: NotificationStatus, selected: boolean): void {
    let next = filters.value.status.filter((value) => value !== status);
    if (selected) {
      if (status === NotificationStatus.FAILED) next = next.filter((value) => value !== NotificationStatus.RETRY_WAIT);
      if (status === NotificationStatus.RETRY_WAIT) next = next.filter((value) => value !== NotificationStatus.FAILED);
      next.push(status);
    }
    filters.value.status = next;
    resetPagination();
  }

  function setChannelFilter(channel: NotificationChannel, selected: boolean): void {
    filters.value.channel = selected
      ? [...filters.value.channel, channel]
      : filters.value.channel.filter((value) => value !== channel);
    resetPagination();
  }

  function resetPagination(): void {
    cursor.value = '';
    nextCursor.value = '';
    cursorHistory.value = [];
  }

  function resetFilters(): void {
    filters.value = defaultFilters();
    resetPagination();
  }

  async function nextPage(): Promise<void> {
    if (!nextCursor.value) return;
    cursorHistory.value.push(cursor.value);
    await loadJobs(nextCursor.value);
  }

  async function previousPage(): Promise<void> {
    if (!cursorHistory.value.length) return;
    await loadJobs(cursorHistory.value.pop() ?? '');
  }

  async function openDrawer(jobId: string): Promise<void> {
    drawerOpen.value = true;
    detail.value = null;
    attempts.value = [];
    const result = await loadJob(jobId);
    if (result !== 'loaded') notice.value = { kind: 'error', message: detailError.value };
  }

  function closeDrawer(): void {
    drawerOpen.value = false;
    detail.value = null;
    attempts.value = [];
    detailError.value = '';
    retryLoading.value = false;
  }

  return {
    jobs,
    filters,
    cursor,
    nextCursor,
    cursorHistory,
    totalCount,
    detail,
    attempts,
    drawerOpen,
    loading: listLoading,
    listLoading,
    detailLoading,
    retryLoading,
    error: listError,
    listError,
    detailError,
    notice,
    hasFilters,
    listErrorMessage,
    loadJobs,
    loadJob,
    getJob,
    jobExists,
    retryJob,
    syncListJob,
    setStatusFilter,
    setChannelFilter,
    resetFilters,
    resetPagination,
    nextPage,
    previousPage,
    openDrawer,
    closeDrawer,
  };
});
