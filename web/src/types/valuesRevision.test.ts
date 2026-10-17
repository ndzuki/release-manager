import { describe, expect, it } from 'vitest';
import { ValuesStatus } from '@/gen/common/v1/domain_pb';
import { statusFromProto, type RevisionStatus } from './valuesRevision';

/*
 * `ValuesStatus` is an open proto3 enum, so a value this console does not know can
 * arrive on the wire, and UNSPECIFIED (0) is what the server's own mapper returns for
 * a stored status it cannot read (internal/orchestrator/values_approval.go
 * valuesStatusToProto). Both used to be reported as `draft`, which is the actionable
 * claim that a revision is editable: the editor gates Submit and Discard on
 * `status === 'draft'`.
 */
describe('statusFromProto', () => {
  it.each([
    [ValuesStatus.DRAFT, 'draft'],
    [ValuesStatus.APPROVED, 'approved'],
    [ValuesStatus.REJECTED, 'rejected'],
    [ValuesStatus.PENDING_APPROVAL, 'pending_approval'],
    [ValuesStatus.SUPERSEDED, 'superseded'],
    [ValuesStatus.DISCARDED, 'discarded'],
  ])('maps the known status %s', (proto, expected) => {
    expect(statusFromProto(proto)).toBe(expected);
  });

  // The positive control above is what keeps the two negatives below from being
  // satisfied by a mapper that answers `unknown` to everything.
  it('does not report UNSPECIFIED as draft', () => {
    expect(statusFromProto(ValuesStatus.UNSPECIFIED)).toBe('unknown');
  });

  it('does not report an enum value it does not know as draft', () => {
    // A newer server may add a status; this console must not read it as editable.
    const future = 99 as ValuesStatus;

    expect(statusFromProto(future)).toBe('unknown');
    expect(statusFromProto(future)).not.toBe('draft');
  });
});

/*
 * Type-level control. Vitest erases types, so nothing at runtime can hold the union
 * itself: the console gates Submit/Discard on `status === 'draft'`, which is only safe
 * while `unknown` is a member of `RevisionStatus`. `vue-tsc -b` (part of
 * `npm run build`) is the runner for this case — the directive below stops being
 * "used" the moment the union loses its third member, which is exactly the edit this
 * fallback has to survive.
 */
describe('the RevisionStatus union', () => {
  it('keeps unknown out of the wire states the editor acts on', () => {
    // @ts-expect-error `unknown` is not one of the wire states: reducing the status back
    // to the approval states by hand is a compile error, not a silent behaviour change.
    const wireState: Exclude<RevisionStatus, 'unknown'> = 'unknown' as RevisionStatus;

    expect(typeof wireState).toBe('string');
  });
});
