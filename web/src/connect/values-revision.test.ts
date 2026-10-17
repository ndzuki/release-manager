import { create } from '@bufbuild/protobuf';
import { describe, expect, it, vi } from 'vitest';
import { ValuesRevisionSchema, ValuesStatus } from '@/gen/common/v1/domain_pb';
import { createValuesRevision, listApprovedValuesRevisions } from './values-revision';

/*
 * CreateValuesRevision is the only writer of a ValuesRevision, and the server
 * refuses a request without a 1-64 character `Idempotency-Key` header
 * (internal/orchestrator/values_revision.go validateValuesIdempotencyKey).
 * The header is not optional plumbing: without it neither the first
 * configuration revision nor a convergence draft can be created from the
 * console, which no unit test noticed because the store mocks this wrapper.
 */
const { rpc, listRpc } = vi.hoisted(() => ({ rpc: vi.fn(), listRpc: vi.fn() }));

vi.mock('./client', () => ({
  orchestratorClient: { createValuesRevision: rpc, listValuesRevisions: listRpc },
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

/*
 * The release-operation form binds to APPROVED revisions only (REQ-056 D10). The
 * server-side status filter is the contract; the client re-filter is what stops a
 * server that ignored the filter from putting a draft or superseded revision on
 * the selector — and therefore into a release operation.
 */
describe('listApprovedValuesRevisions', () => {
  it('asks the server for APPROVED and drops any other status it returns', async () => {
    listRpc.mockReset().mockResolvedValue({
      items: [
        create(ValuesRevisionSchema, { id: 'rev-approved', releaseDefinitionId: 'def-1', status: ValuesStatus.APPROVED }),
        create(ValuesRevisionSchema, { id: 'rev-draft', releaseDefinitionId: 'def-1', status: ValuesStatus.DRAFT }),
        create(ValuesRevisionSchema, { id: 'rev-superseded', releaseDefinitionId: 'def-1', status: ValuesStatus.SUPERSEDED }),
      ],
    });

    const revisions = await listApprovedValuesRevisions('def-1');

    const sent = listRpc.mock.calls[0]?.[0] as { releaseDefinitionId: string; status: ValuesStatus };
    expect(sent.releaseDefinitionId).toBe('def-1');
    expect(sent.status).toBe(ValuesStatus.APPROVED);
    expect(revisions.map((revision) => revision.id)).toEqual(['rev-approved']);
  });
});
