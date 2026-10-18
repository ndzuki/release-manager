import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import EmergencyResultPanel from './EmergencyResultPanel.vue';
import type { EmergencyResultDisplay } from '@/features/emergency/model';

/*
 * W21 (UX-033). `dd` had no `overflow-wrap`, so a long image reference — one token,
 * no spaces, and typically `registry/namespace/repo@sha256:<64 hex>` — overflowed its
 * grid track and widened the whole panel/page. `anywhere` (not `break-word`) is the
 * load-bearing choice: it also lowers the track's min-content contribution, which is
 * what stops the `minmax(14rem, 1fr)` track from growing in the first place.
 *
 * happy-dom does not evaluate scoped CSS, so the stylesheet half reads the SFC source,
 * following the convention Toast.test.ts established.
 */
function result(overrides: Partial<EmergencyResultDisplay> = {}): EmergencyResultDisplay {
  return {
    opType: 'SET_CONTAINER_IMAGE',
    convergencePolicy: 'REQUIRE_PROMOTION',
    requested: true,
    before: { case: 'image', container: 'app', imageReference: 'registry.local/app:v1' },
    after: { case: 'image', container: 'app', imageReference: 'registry.local/app:v2' },
    effectStatus: 'APPLIED',
    convergenceTasks: [],
    revertStatus: '',
    reconciledByOperationId: '',
    ...overrides,
  };
}

function styleBlock(): string {
  const source = readFileSync(resolve(__dirname, 'EmergencyResultPanel.vue'), 'utf8');
  const withoutComments = source.replace(/\/\*[\s\S]*?\*\//g, '');
  const match = withoutComments.match(/<style scoped>([\s\S]*?)<\/style>/);
  if (!match) throw new Error('EmergencyResultPanel.vue has no scoped style block');
  return match[1]!;
}

function mountPanel(value: EmergencyResultDisplay) {
  return mount(EmergencyResultPanel, {
    props: {
      result: value,
      operationState: 'succeeded',
      operationEffectStatus: 'APPLIED',
      observationStatus: 'resolved',
      canCreateValuesRevision: true,
    },
  });
}

describe('EmergencyResultPanel long-token wrapping (W21)', () => {
  it('renders the long image reference unchanged (wrapping is CSS, not truncation)', () => {
    const longRef = `registry.example.com/team/sub-team/app@sha256:${'a'.repeat(64)}`;
    const wrapper = mountPanel(result({
      before: { case: 'image', container: 'app', imageReference: 'registry.example.com/team/app:v1' },
      after: { case: 'image', container: 'app', imageReference: longRef },
    }));

    const values = wrapper.findAll('.result-grid dd').map((cell) => cell.text());
    expect(values.some((text) => text.includes(longRef))).toBe(true);
  });

  it('wraps long tokens anywhere inside a result cell', () => {
    const style = styleBlock();

    const rule = style.match(/\.result-grid dd\s*\{([^}]*)\}/);
    expect(rule, '.result-grid dd rule is gone; this assertion needs updating').not.toBeNull();
    expect(rule![1]).toContain('overflow-wrap: anywhere');
    // `break-word` would keep the min-content floor and still overflow the track.
    expect(rule![1]).not.toContain('break-word');
  });
});
