import { create } from '@bufbuild/protobuf';
import { describe, expect, it, vi } from 'vitest';
import { ValuesRevisionSchema, ValuesStatus } from '@/gen/common/v1/domain_pb';
import { createValuesRevision } from './values-revision';

/*
 * CreateValuesRevision is the only writer of a ValuesRevision, and the server
 * refuses a request without a 1-64 character `Idempotency-Key` header
 * (internal/orchestrator/values_revision.go validateValuesIdempotencyKey).
 * The header is not optional plumbing: without it neither the first
 * configuration revision nor a convergence draft can be created from the
 * console, which no unit test noticed because the store mocks this wrapper.
 */
const { rpc } = vi.hoisted(() => ({ rpc: vi.fn() }));

vi.mock('./client', () => ({
  orchestratorClient: { createValuesRevision: rpc },
}));

function draftResponse() {
  return {
    revision: create(ValuesRevisionSchema, {
      id: 'rev-1',
      releaseDefinitionId: 'def-1',
      status: ValuesStatus.DRAFT,
    }),
  };
}

function sentHeaders(call: number): Headers {
  return (rpc.mock.calls[call]?.[1] as { headers: Headers }).headers;
}

describe('createValuesRevision', () => {
  it('sends the mandatory Idempotency-Key header', async () => {
    rpc.mockReset().mockResolvedValue(draftResponse());

    await createValuesRevision({
      releaseDefinitionId: 'def-1',
      parentRevisionId: 'parent-1',
      document: '{"replicaCount":1}',
      secretRefs: [],
      expectedParentVersion: 1,
    });

    const key = sentHeaders(0).get('Idempotency-Key');
    expect(key).toBeTruthy();
    expect(key!.length).toBeGreaterThan(0);
    expect(key!.length).toBeLessThanOrEqual(64);
  });

  it('keeps a caller-supplied key so a replay stays idempotent', async () => {
    rpc.mockReset().mockResolvedValue(draftResponse());

    await createValuesRevision({
      releaseDefinitionId: 'def-1',
      parentRevisionId: 'parent-1',
      document: '{"replicaCount":1}',
      secretRefs: [],
      expectedParentVersion: 1,
      prepareToken: 'token-1',
      idempotencyKey: 'stable-key-1',
    });

    expect(sentHeaders(0).get('Idempotency-Key')).toBe('stable-key-1');
  });
});
