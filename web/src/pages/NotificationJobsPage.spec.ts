import { createPinia, setActivePinia } from 'pinia';
import { Code, ConnectError } from '@connectrpc/connect';
import { mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const { listNotificationJobs, getNotificationJob, retryNotificationJob } = vi.hoisted(() => ({
	listNotificationJobs: vi.fn(),
	getNotificationJob: vi.fn(),
	retryNotificationJob: vi.fn(),
}));
vi.mock('@/connect/client', () => ({ notificationJobsClient: { listNotificationJobs, getNotificationJob, retryNotificationJob } }));

import NotificationJobsPage from './NotificationJobsPage.vue';

describe('NotificationJobsPage', () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    listNotificationJobs.mockReset();
    getNotificationJob.mockReset();
    retryNotificationJob.mockReset();
  });

  it('renders the unfiltered empty state', async () => {
    listNotificationJobs.mockResolvedValue({ jobs: [], nextPageToken: '', totalCount: 0 });
    const wrapper = mount(NotificationJobsPage);
    await vi.waitFor(() => expect(wrapper.text()).toContain('暂无通知任务'));
  });

  it('renders the filtered empty state and clears filters', async () => {
    listNotificationJobs.mockResolvedValue({ jobs: [], nextPageToken: '', totalCount: 0 });
    const wrapper = mount(NotificationJobsPage);
    await vi.waitFor(() => expect(wrapper.text()).toContain('暂无通知任务'));
    const search = wrapper.get('input[type="search"]');
    await search.setValue('admin@example.com');
    await search.trigger('input');
    await vi.waitFor(() => expect(wrapper.text()).toContain('当前筛选条件下无匹配结果'));
    expect(wrapper.text()).toContain('清除筛选');
  });

  it('renders an expired replay ancestor as plain text on first detail load', async () => {
    const job = {
      jobId: 'job-b',
      operationId: 'operation-1',
      channel: 1,
      displayRecipient: 'https://hooks.example.com/***',
      replayOf: 'job-a',
      replayedBy: [],
      status: 5,
      attempts: 0,
      lastError: '',
      version: 1n,
    };
    listNotificationJobs.mockResolvedValue({ jobs: [job], nextPageToken: '', totalCount: 1 });
    getNotificationJob
      .mockResolvedValueOnce({ job, attempts: [] })
      .mockRejectedValueOnce(new ConnectError('job_not_found', Code.NotFound));
    const wrapper = mount(NotificationJobsPage);
    await vi.waitFor(() => expect(wrapper.text()).toContain('operation-1'));

    await wrapper.get('tbody tr').trigger('click');

    await vi.waitFor(() => expect(document.body.textContent).toContain('job-a（已过期）'));
    expect(Array.from(document.body.querySelectorAll('button')).some((button) => button.textContent === 'job-a')).toBe(false);
  });

  it('renders a recoverable load error banner', async () => {
    listNotificationJobs.mockRejectedValue(new Error('network unavailable'));
    const wrapper = mount(NotificationJobsPage);
    await vi.waitFor(() => expect(wrapper.get('[role="alert"]').text()).toContain('加载失败，请检查网络连接后重试'));
    expect(wrapper.get('[role="alert"] button').text()).toBe('Retry');
  });
});
