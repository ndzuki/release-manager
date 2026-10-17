import { Code, ConnectError } from '@connectrpc/connect';
import { describe, expect, it, vi } from 'vitest';
import { decodeApprovedAnnotationKeys, decodePromotionMappings, mapDefinitionError } from './definition-api';
import type { UpdateReleaseDefinitionRequest } from '@/gen/orchestrator/v1/orchestrator_pb';
import type {
  CreateReleaseDefinitionRequest,
  DisableReleaseDefinitionRequest,
} from '@/gen/orchestrator/v1/orchestrator_pb';

function failed(message: string, code: Code) {
  return new ConnectError(message, code);
}

function bytes(value: string): Uint8Array {
  return new TextEncoder().encode(value);
}

describe('decodePromotionMappings', () => {
  it('treats empty bytes as "not configured"', () => {
    expect(decodePromotionMappings(new Uint8Array())).toEqual({ value: null, violation: null });
  });

  it('decodes the wire shape, defaulting the optional container', () => {
    const decoded = decodePromotionMappings(
      bytes('[{"workload_kind":"Deployment","workload_name":"api","field":"image","values_path":"image.tag"}]'),
    );

    expect(decoded.violation).toBeNull();
    expect(decoded.value).toEqual([
      { workloadKind: 'Deployment', workloadName: 'api', container: '', field: 'image', valuesPath: 'image.tag' },
    ]);
  });

  // The proto calls an undecodable value a contract violation, and "parseable but
  // wrong shape" is undecodable for our purposes.
  it.each([
    ['not json at all', 'not json at all'],
    ['an object', '{}'],
    ['a bare string', '"x"'],
    ['a non-object element', '[1]'],
    ['an element missing a required key', '[{"workload_kind":"Deployment"}]'],
  ])('reports %s as a violation', (_label, raw) => {
    const decoded = decodePromotionMappings(bytes(raw));

    expect(decoded.value).toBeNull();
    expect(decoded.violation).not.toBeNull();
  });
});

/*
 * TASK-274: the definition's `approved_annotation_keys` JSON bytes are the only
 * read that carries keys approved but NOT yet observed on the workload, and the
 * emergency editor's whole "add a key that does not exist yet" path depends on
 * this decode. The shape is fixed by the Go type it marshals
 * (internal/store/store.go:1149, `key` / `scope` / `promotion_values_path`), so
 * a drifted field name must be loud here, not an empty whitelist downstream.
 */
describe('decodeApprovedAnnotationKeys', () => {
  it('treats empty bytes as "not configured"', () => {
    expect(decodeApprovedAnnotationKeys(new Uint8Array())).toEqual({ value: null, violation: null });
  });

  it('decodes key + scope pairs and defaults the optional promotion path', () => {
    const decoded = decodeApprovedAnnotationKeys(
      bytes(
        '[{"key":"tier","scope":"WORKLOAD_METADATA"},{"key":"zone","scope":"POD_TEMPLATE_METADATA","promotion_values_path":"labels.zone"}]',
      ),
    );

    expect(decoded.violation).toBeNull();
    /*
     * `promotionValuesPath` has NO production consumer today (TASK-274 leftover 10.4):
     * the emergency editor offers key/scope only, and nothing else reads the view. The
     * assertion is kept on purpose — the field is part of the Go type's JSON shape
     * (internal/store/store.go:1149), so a drifted JSON name has to stay loud here for
     * the day a consumer appears rather than silently decoding to ''.
     */
    expect(decoded.value).toEqual([
      { key: 'tier', scope: 'WORKLOAD_METADATA', promotionValuesPath: '' },
      { key: 'zone', scope: 'POD_TEMPLATE_METADATA', promotionValuesPath: 'labels.zone' },
    ]);
  });

  // "parseable but wrong shape" is a contract violation here too: silently
  // degrading it to [] would hide every approved key from the emergency form.
  it.each([
    ['not json at all', 'not json at all'],
    ['an object', '{}'],
    ['a bare string', '"x"'],
    ['a non-object element', '[1]'],
    ['an element missing key', '[{"scope":"WORKLOAD_METADATA"}]'],
    ['an element with a non-string scope', '[{"key":"tier","scope":7}]'],
  ])('reports %s as a violation', (_label, raw) => {
    const decoded = decodeApprovedAnnotationKeys(bytes(raw));

    expect(decoded.value).toBeNull();
    expect(decoded.violation).not.toBeNull();
  });
});

describe('getDefinition', () => {
  it('sends the id and exposes the decoded whitelist on the view', async () => {
    const { create } = await import('@bufbuild/protobuf');
    const { GetReleaseDefinitionRequestSchema } = await import('@/gen/orchestrator/v1/orchestrator_pb');
    const { ReleaseDefinitionSchema } = await import('@/gen/common/v1/domain_pb');
    const client = await import('./client');
    const spy = vi.spyOn(client.orchestratorClient, 'getReleaseDefinition').mockResolvedValue({
      definition: create(ReleaseDefinitionSchema, {
        id: 'def-1',
        approvedAnnotationKeys: bytes('[{"key":"tier","scope":"WORKLOAD_METADATA"}]'),
      }),
    } as never);

    const { getDefinition } = await import('./definition-api');
    const view = await getDefinition('def-1');

    const request = spy.mock.calls[0][0] as unknown as {
      $typeName: string;
      definitionId: string;
    };
    expect(request.$typeName).toBe(GetReleaseDefinitionRequestSchema.typeName);
    expect(request.definitionId).toBe('def-1');
    expect(view.approvedAnnotationKeys).toEqual([
      { key: 'tier', scope: 'WORKLOAD_METADATA', promotionValuesPath: '' },
    ]);
    expect(view.approvedAnnotationKeysViolation).toBeNull();
    spy.mockRestore();
  });
});

/*
 * TASK-301: the console could show and edit a definition but could not CREATE one
 * nor disable it, although both RPCs exist (orchestrator/v1:1257/:1283). These cases
 * pin the request the server receives and the failure classification the page shows.
 *
 * The transport is BINARY (`useBinaryFormat: true` in client.ts), so the request is
 * round-tripped through the same codec the transport uses instead of being trusted
 * as an in-memory object.
 */
describe('createDefinition', () => {
  it('sends the typed create contract and maps the returned definition', async () => {
    const { create } = await import('@bufbuild/protobuf');
    const { CreateReleaseDefinitionRequestSchema } = await import('@/gen/orchestrator/v1/orchestrator_pb');
    const { ReleaseDefinitionSchema } = await import('@/gen/common/v1/domain_pb');
    const client = await import('./client');
    const spy = vi.spyOn(client.orchestratorClient, 'createReleaseDefinition').mockResolvedValue({
      definition: create(ReleaseDefinitionSchema, {
        id: 'def-9',
        name: 'release-a',
        customerId: 'cust-1',
        clusterId: 'cluster-1',
        status: 'active',
        version: 1n,
      }),
    } as never);

    const { createDefinition } = await import('./definition-api');
    const created = await createDefinition({
      customerId: 'cust-1',
      clusterId: 'cluster-1',
      namespace: 'ns',
      releaseName: 'release-a',
      chartName: 'chart',
      enabled: true,
      promotionMappings: [
        { workloadKind: 'Deployment', workloadName: 'api', container: 'app', field: 'image', valuesPath: 'image.tag' },
      ],
    });

    const request = spy.mock.calls[0]?.[0] as unknown as CreateReleaseDefinitionRequest;
    expect(request.$typeName).toBe(CreateReleaseDefinitionRequestSchema.typeName);
    expect(request.customerId).toBe('cust-1');
    expect(request.clusterId).toBe('cluster-1');
    expect(request.namespace).toBe('ns');
    expect(request.releaseName).toBe('release-a');
    expect(request.chartName).toBe('chart');
    expect(request.enabled).toBe(true);
    expect(request.promotionMappings).toHaveLength(1);

    const { fromBinary, toBinary } = await import('@bufbuild/protobuf');
    const decoded = fromBinary(
      CreateReleaseDefinitionRequestSchema,
      toBinary(CreateReleaseDefinitionRequestSchema, request),
    );
    expect(decoded.customerId).toBe('cust-1');
    expect(decoded.enabled).toBe(true);
    expect(decoded.promotionMappings[0]?.workloadName).toBe('api');

    expect(created.id).toBe('def-9');
    expect(created.status).toBe('active');
    expect(created.version).toBe(1n);
  });

  /*
   * `enabled: false` creates a DRAFT (`definition.go: `if !msg.GetEnabled()`) and
   * proto3 bools have no presence, so the flag is conveyed by the field being ABSENT:
   * tag for field 6 (varint) is 0x30. A wrapper that dropped the flag would send the
   * same bytes as `true` for a caller asking for a draft, so both directions are pinned.
   */
  it('conveys enabled: false by omitting field 6, which the server reads as draft', async () => {
    const { create, toBinary } = await import('@bufbuild/protobuf');
    const { CreateReleaseDefinitionRequestSchema } = await import('@/gen/orchestrator/v1/orchestrator_pb');
    const { ReleaseDefinitionSchema } = await import('@/gen/common/v1/domain_pb');
    const client = await import('./client');
    const spy = vi.spyOn(client.orchestratorClient, 'createReleaseDefinition').mockResolvedValue({
      definition: create(ReleaseDefinitionSchema, { id: 'def-9', status: 'draft', version: 1n }),
    } as never);

    const { createDefinition } = await import('./definition-api');
    const input = { customerId: 'c', clusterId: 'k', releaseName: 'r' };
    await createDefinition({ ...input, enabled: false });
    await createDefinition({ ...input, enabled: true });

    const hex = (index: number) =>
      Array.from(
        toBinary(
          CreateReleaseDefinitionRequestSchema,
          spy.mock.calls[index]?.[0] as unknown as CreateReleaseDefinitionRequest,
        ),
      )
        .map((byte) => byte.toString(16).padStart(2, '0'))
        .join('');

    /*
     * The negative ("no field 6") is only meaningful next to the positive: the two
     * requests are byte-identical EXCEPT that the enabled one ends with the field-6
     * varint (tag 0x30, value 0x01). An "absent" assertion alone would also pass for a
     * wrapper that dropped the flag in both directions.
     */
    expect(hex(0)).not.toContain('30');
    expect(hex(1)).toBe(`${hex(0)}3001`);
  });

  it('propagates a duplicate refusal and classifies it as a retryable-free duplicate', async () => {
    const failure = new ConnectError('definition already exists for customer=c cluster=k', Code.AlreadyExists);
    const client = await import('./client');
    vi.spyOn(client.orchestratorClient, 'createReleaseDefinition').mockRejectedValue(failure);

    const { createDefinition } = await import('./definition-api');
    await expect(
      createDefinition({ customerId: 'c', clusterId: 'k', releaseName: 'r', enabled: true }),
    ).rejects.toBe(failure);
    const mapped = mapDefinitionError(failure);
    expect(mapped.code).toBe('duplicate');
    expect(mapped.retryable).toBe(false);
  });
});

describe('disableDefinition', () => {
  it('sends the definition id with the version it read and returns the disabled view', async () => {
    const { create } = await import('@bufbuild/protobuf');
    const { DisableReleaseDefinitionRequestSchema } = await import('@/gen/orchestrator/v1/orchestrator_pb');
    const { ReleaseDefinitionSchema } = await import('@/gen/common/v1/domain_pb');
    const client = await import('./client');
    const spy = vi.spyOn(client.orchestratorClient, 'disableReleaseDefinition').mockResolvedValue({
      definition: create(ReleaseDefinitionSchema, { id: 'def-1', name: 'release-a', status: 'disabled', version: 4n }),
    } as never);

    const { disableDefinition } = await import('./definition-api');
    const disabled = await disableDefinition('def-1', 3n);

    const request = spy.mock.calls[0]?.[0] as unknown as DisableReleaseDefinitionRequest;
    expect(request.$typeName).toBe(DisableReleaseDefinitionRequestSchema.typeName);
    expect(request.definitionId).toBe('def-1');
    expect(request.expectedVersion).toBe(3n);

    const { fromBinary, toBinary } = await import('@bufbuild/protobuf');
    const decoded = fromBinary(
      DisableReleaseDefinitionRequestSchema,
      toBinary(DisableReleaseDefinitionRequestSchema, request),
    );
    expect(decoded.definitionId).toBe('def-1');
    expect(decoded.expectedVersion).toBe(3n);

    expect(disabled.status).toBe('disabled');
    expect(disabled.name).toBe('release-a');
  });

  // DisableReleaseDefinition writes its lost race with FAILED_PRECONDITION + the
  // `optimistic_lock_conflict` token, so the token branch is what keeps the retry
  // hint (and the reload) alive.
  it('maps a lost race to a retryable conflict and an unknown definition to not_found', async () => {
    const conflict = new ConnectError('optimistic_lock_conflict: definition version changed', Code.FailedPrecondition);
    const client = await import('./client');
    vi.spyOn(client.orchestratorClient, 'disableReleaseDefinition').mockRejectedValue(conflict);

    const { disableDefinition } = await import('./definition-api');
    await expect(disableDefinition('def-1', 3n)).rejects.toBe(conflict);
    expect(mapDefinitionError(conflict)).toMatchObject({ code: 'conflict', retryable: true });

    expect(mapDefinitionError(new ConnectError('definition "x" not found', Code.NotFound)).code).toBe('not_found');
  });
});

describe('mapDefinitionError', () => {
  // definition.go writes no X-Reason-Code but prefixes its refusals with a machine
  // token, so that token is the carrier (never a free-text substring).
  it('classifies the optimistic-lock refusal as a retryable conflict', () => {
    const failure = mapDefinitionError(failed('optimistic_lock_conflict: expected version 3, current 5', Code.FailedPrecondition));

    expect(failure.code).toBe('conflict');
    expect(failure.retryable).toBe(true);
  });

  it('does not treat an unrelated precondition as a conflict token', () => {
    // Still a conflict by code, but the message must not be matched as free text.
    expect(mapDefinitionError(failed('definition is disabled', Code.FailedPrecondition)).code).toBe('conflict');
  });

  it.each([
    [Code.NotFound, 'not_found'],
    [Code.AlreadyExists, 'duplicate'],
    [Code.PermissionDenied, 'permission_denied'],
    [Code.InvalidArgument, 'invalid_input'],
    [Code.Internal, 'unavailable'],
  ])('maps %s to %s', (code, expected) => {
    expect(mapDefinitionError(failed('whatever', code)).code).toBe(expected);
  });

  it('always carries a correlation line', () => {
    const failure = mapDefinitionError(new ConnectError('boom', Code.Internal, { 'X-Request-ID': 'req-3' }));

    expect(failure.details).toContain('requestId=req-3');
  });
});

/*
 * TASK-214: clearing a list must reach the server as an explicit replacement. proto3
 * repeated fields have no presence, so sending an empty `promotionMappings` would read as
 * "absent" and the server would leave the stored mappings alone while answering 200 — the
 * silent false success this test exists to prevent.
 */
describe('updateReleaseDefinition presence', () => {
  it('sends an empty replacement wrapper when the mappings are cleared', async () => {
    const { create } = await import('@bufbuild/protobuf');
    const { UpdateReleaseDefinitionRequestSchema } = await import('@/gen/orchestrator/v1/orchestrator_pb');
    const { ReleaseDefinitionSchema } = await import('@/gen/common/v1/domain_pb');
    const client = await import('./client');
    const spy = vi
      .spyOn(client.orchestratorClient, 'updateReleaseDefinition')
      .mockResolvedValue({ definition: create(ReleaseDefinitionSchema, { id: 'def-1' }) } as never);

    const { updateDefinition } = await import('./definition-api');
    await updateDefinition({
      definitionId: 'def-1',
      expectedVersion: 3n,
      namespace: 'ns',
      releaseName: '',
      chartName: 'chart',
      promotionMappings: [],
    });

    /*
     * The client method accepts either an init object or a message; the call site builds a
     * real message with `create(...)`, and this test needs the message so it can encode it
     * (`toBinary`). Reading it through a structural type instead would erase exactly the
     * presence the assertions below are about.
     */
    const request = spy.mock.calls[0][0] as unknown as UpdateReleaseDefinitionRequest;
    expect(request.promotionMappingsReplace?.items).toEqual([]);
    expect(request.promotionMappings).toEqual([]);
    // present-and-empty must stay present: that is what clears the field server-side.
    expect(request.releaseName).toBe('');

    /*
     * The in-memory shape is not the contract: the SERVER only sees bytes. Encode the
     * request the client actually built and assert the presence is on the wire --
     * field 3 (release_name) present and empty, field 11 (the replace wrapper) present
     * with no items, and field 9 (the legacy list) absent. A message that agrees in
     * memory while losing presence during serialization would still pass every other
     * assertion in this file.
     */
    const { toBinary } = await import('@bufbuild/protobuf');
    const bytes = toBinary(UpdateReleaseDefinitionRequestSchema, request);
    const hex = Array.from(bytes)
      .map((b) => b.toString(16).padStart(2, '0'))
      .join('');
    expect(hex).toContain('1a00'); // field 3, length 0 -> clear release_name
    expect(hex).toContain('5a00'); // field 11, length 0 -> replace mappings with none
    expect(hex).not.toContain('4a'); // field 9 must not be sent alongside the wrapper
    spy.mockRestore();
  });
});
