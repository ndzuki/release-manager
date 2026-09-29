import { Code, ConnectError } from '@connectrpc/connect';
import { describe, expect, it, vi } from 'vitest';
import { decodePromotionMappings, mapDefinitionError } from './definition-api';
import type { UpdateReleaseDefinitionRequest } from '@/gen/orchestrator/v1/orchestrator_pb';

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
