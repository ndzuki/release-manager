import { Code, ConnectError } from '@connectrpc/connect';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { auditClient } from '@/connect/client';
import { emptyAuditFilters, useAuditStore } from './audit';

vi.mock('@/connect/client', () => ({
  auditClient: {
    queryAuditEvents: vi.fn(),
    exportAuditEvents: vi.fn(),
  },
  authClient: { switchOrganization: vi.fn() },
  setAuthErrorHandler: vi.fn(),
}));

const exportAuditEvents = vi.mocked(auditClient.exportAuditEvents);

// TASK-339 review P2: the server refuses an all-organizations export with
// INVALID_ARGUMENT / all_organizations_not_supported because an export row
// carries exactly one organization. That reason code must get its own localized
// copy — falling through to the InvalidArgument branch showed the raw English
// server message in the console.
describe('audit store export scope', () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    exportAuditEvents.mockReset();
  });

  it('localizes the all_organizations_not_supported rejection', async () => {
    exportAuditEvents.mockRejectedValue(
      new ConnectError('all_organizations is not supported for audit exports', Code.InvalidArgument, {
        'X-Reason-Code': 'all_organizations_not_supported',
      }),
    );
    const store = useAuditStore();
    store.setFilters(emptyAuditFilters());

    await store.exportEvents('', true);

    expect(exportAuditEvents).toHaveBeenCalledTimes(1);
    expect(exportAuditEvents.mock.calls[0][0].filter).toMatchObject({
      allOrganizations: true,
      organizationId: '',
    });
    expect(store.error?.reason).toBe('all_organizations_not_supported');
    expect(store.error?.message).toBe('导出只支持单个组织：请先选择具体组织再导出。');
    // The raw server message must not leak through as the user-facing copy.
    expect(store.error?.message).not.toContain('all_organizations');
  });
});
