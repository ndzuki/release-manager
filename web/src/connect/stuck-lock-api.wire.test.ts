import { ConnectError } from '@connectrpc/connect';
import { fromBinary } from '@bufbuild/protobuf';
import {
  ListStuckLocksRequestSchema,
  ReleaseEmergencyLockRequestSchema,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { listStuckLocks, releaseEmergencyLock } from './stuck-lock-api';

/*
 * Wire-shape tests: every UI test mocks this module, so the mapping from the
 * request object to the Connect message (and the numeric ReleaseMode) had no guard
 * at all. These go through the real client and a stubbed fetch.
 */
function stubFetch(body: unknown, status = 500) {
  const calls: Array<{ url: string; bytes: Uint8Array }> = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = new Request(input as RequestInfo, init);
    calls.push({ url: request.url, bytes: new Uint8Array(await request.arrayBuffer()) });
    return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
  }));
  return calls;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('stuck lock wire shape', () => {
  it('sends the release definition filter for the list', async () => {
    const calls = stubFetch({ code: 'internal', message: 'boom' });

    await expect(listStuckLocks('def-3')).rejects.toBeInstanceOf(ConnectError);

    expect(calls[0]!.url).toContain('/orchestrator.v1.OrchestratorService/ListStuckLocks');
    // The transport sends the binary format, so decode with the same schema the
    // server will use.
    expect(fromBinary(ListStuckLocksRequestSchema, calls[0]!.bytes).releaseDefinitionId).toBe('def-3');
  });

  it('maps the mode to its numeric enum and defaults evidence to empty', async () => {
    const calls = stubFetch({ code: 'internal', message: 'boom' }, 500);

    await expect(
      releaseEmergencyLock({ intentId: 'intent-9', reason: 'why', mode: 'AUDITED_OVERRIDE' }),
    ).rejects.toBeInstanceOf(ConnectError);

    expect(calls[0]!.url).toContain('/ReleaseEmergencyLock');
    const sent = fromBinary(ReleaseEmergencyLockRequestSchema, calls[0]!.bytes);
    expect(sent.intentId).toBe('intent-9');
    expect(sent.reason).toBe('why');
    expect(sent.mode).toBe(2);
    expect(sent.evidence).toBe('');
  });

  it('maps NOT_APPLIED_PROVEN to mode 1', async () => {
    const calls = stubFetch({ code: 'internal', message: 'boom' }, 500);

    await expect(
      releaseEmergencyLock({ intentId: 'intent-9', reason: 'why', mode: 'NOT_APPLIED_PROVEN', evidence: 'seen' }),
    ).rejects.toBeInstanceOf(ConnectError);

    const sent = fromBinary(ReleaseEmergencyLockRequestSchema, calls[0]!.bytes);
    expect(sent.mode).toBe(1);
    expect(sent.evidence).toBe('seen');
  });
});
