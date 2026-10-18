import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import OperatorTable from './OperatorTable.vue';
import type { OperatorSummary } from '@/types/operator';

/*
 * W21 (UX-031). The table set `white-space: nowrap` on every `th`/`td`, which forced
 * the whole table to its fully unwrapped width: one long operator name (or a uuid,
 * which has no break opportunity at all) pushed the table into a horizontal scrollbar
 * even on a desktop viewport. The fix is per-cell, so the assertions are too: the name
 * wraps anywhere, the timestamps keep nowrap (a date split across two lines reads as
 * two unrelated values), and the blanket rule is gone.
 *
 * happy-dom does not evaluate scoped CSS, so the stylesheet half reads the SFC source
 * — the same convention Toast.test.ts uses for its reduced-motion rule.
 */
function operator(overrides: Partial<OperatorSummary> = {}): OperatorSummary {
  return {
    id: 'operator-1',
    name: 'operator-one',
    customerId: 'customer-1',
    clusterId: 'cluster-1',
    clusterName: 'Staging',
    lifecycleStatus: 'active',
    sessionStatus: 'online',
    sessionStatusReason: null,
    lastHeartbeat: '2026-10-18T03:00:00.000Z',
    registeredAt: '2026-10-01T00:00:00.000Z',
    supersededAt: null,
    revokedAt: null,
    ...overrides,
  };
}

function styleBlock(): string {
  const source = readFileSync(resolve(__dirname, 'OperatorTable.vue'), 'utf8');
  const withoutComments = source.replace(/\/\*[\s\S]*?\*\//g, '');
  const match = withoutComments.match(/<style scoped>([\s\S]*?)<\/style>/);
  if (!match) throw new Error('OperatorTable.vue has no scoped style block');
  return match[1]!;
}

describe('OperatorTable name column wrapping (W21)', () => {
  it('marks the name cell and the two timestamp cells explicitly', () => {
    const wrapper = mount(OperatorTable, { props: { operators: [operator()], canRevoke: false } });

    const cells = wrapper.findAll('tbody td');
    expect(cells[0]!.classes()).toContain('operators__name');
    expect(cells[3]!.classes()).toContain('operators__time');
    expect(cells[4]!.classes()).toContain('operators__time');
    // Status and action cells are short enough to wrap; they must not inherit a nowrap
    // from a blanket rule.
    expect(cells[1]!.classes()).not.toContain('operators__time');
    expect(cells[5]!.classes()).not.toContain('operators__time');
  });

  it('lets a long name wrap instead of widening the table', () => {
    const longName = 'release-operator-eu-west-1-production-cluster-primary-4f9c8b7d6-x2vqm';
    const wrapper = mount(OperatorTable, { props: { operators: [operator({ name: longName })], canRevoke: false } });

    expect(wrapper.get('.operators__name').text()).toBe(longName);
    expect(styleBlock()).toMatch(/\.operators__name\s*\{[^}]*overflow-wrap:\s*anywhere/);
  });

  it('keeps the shared cell rule free of the blanket nowrap', () => {
    const style = styleBlock();

    const shared = style.match(/\.operators th,\s*\.operators td\s*\{([^}]*)\}/);
    expect(shared, 'the shared th/td rule is gone; this assertion needs updating').not.toBeNull();
    expect(shared![1]).not.toContain('white-space');
    // Timestamps are the only nowrap survivors, and they say why.
    expect(style).toMatch(/\.operators__time\s*\{[^}]*white-space:\s*nowrap/);
  });
});
