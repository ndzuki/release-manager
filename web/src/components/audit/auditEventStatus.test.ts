import { describe, expect, it } from 'vitest';
import { t } from '@/i18n/messages';
import { auditStatusLabel, auditStatusTone } from './auditEventStatus';

/*
 * TASK-394: the audit "result" column answers one question — did it work? The tone
 * map is pinned against the values the server actually writes, each with its producer
 * (the full source list lives in auditEventStatus.ts). A test that only sampled
 * "succeeded" would let a refusal render green, which is the failure this table cannot
 * afford: the audit trail is evidence.
 */
describe('auditStatusTone', () => {
  it('renders success outcomes in the ok tone', () => {
    for (const status of ['success', 'succeeded', 'trusted']) {
      expect(auditStatusTone(status), status).toBe('ok');
    }
  });

  it('renders refusals and failures in the er tone', () => {
    // failed: internal/orchestrator/audit.go:19
    // rejected: internal/store/store.go:23 (trust verification, written as the audit
    //   status by internal/orchestrator/service.go:1758)
    // blocked: internal/orchestrator/admission.go:91 (admission refusal)
    for (const status of ['failed', 'rejected', 'blocked']) {
      expect(auditStatusTone(status), status).toBe('er');
    }
  });

  it('keeps every other server value neutral instead of guessing an outcome', () => {
    for (const status of [
      'accepted',
      'would_block',
      'policy_warning',
      'signature_missing',
      'verification_unavailable',
      'pending',
      'preflight',
      'queued',
      'running',
      'cancelling',
      'cancelled',
      'timeout',
      '',
      'a_status_from_a_future_server',
    ]) {
      expect(auditStatusTone(status), status).toBe('neutral');
    }
  });
});

describe('auditStatusLabel', () => {
  it('labels the known outcome values from the catalog', () => {
    expect(auditStatusLabel('succeeded')).toBe(t('status.audit.succeeded'));
    expect(auditStatusLabel('failed')).toBe(t('status.audit.failed'));
    expect(auditStatusLabel('accepted')).toBe(t('status.audit.accepted'));
    expect(auditStatusLabel('rejected')).toBe(t('audit.result.rejected'));
    expect(auditStatusLabel('blocked')).toBe(t('audit.result.blocked'));
    expect(auditStatusLabel('trusted')).toBe(t('audit.result.trusted'));
  });

  it('keeps an unknown value visible instead of rendering an empty cell', () => {
    expect(auditStatusLabel('a_status_from_a_future_server')).toBe('a_status_from_a_future_server');
    expect(auditStatusLabel('')).toBe('');
  });
});
