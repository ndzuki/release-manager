import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import { nextTick } from 'vue';
import { t } from '@/i18n/messages';
import type { AuditEvent } from '@/gen/audit/v1/audit_pb';
import AuditEventTable from './AuditEventTable.vue';

/*
 * AuditEventTable's pager contract (TASK-300).
 *
 * The table used to hand-roll its two buttons inside a plain `<div aria-label>`, so
 * the region was an UNNAMED navigation landmark (or, as a div, no landmark at all)
 * and the wording and disabled rule lived in a second place next to Pagination's.
 * It now renders the shared primitive, and only three things are still this table's:
 *
 *   - the accessible name it passes down (`audit.table.pagination`), which the
 *     primitive would otherwise default to its generic catalog label;
 *   - reachability, with `loading` folded in the way the primitive documents
 *     (`:has-prev="hasPrev && !loading"`), because the pager cannot know a page is
 *     in flight and a second click would advance the cursor twice;
 *   - the public event names. AuditPage binds `@previous` / `@next` while the
 *     primitive emits `prev` / `next`, so the bridge is load-bearing and these tests
 *     assert the names the consumer actually listens for.
 *
 * `trigger('click')` is deliberately NOT how the disabled directions are probed here:
 * VTU skips the dispatch on a disabled element entirely, so "it did not emit" would
 * hold even if the disabled state were wrong. The disabled state is asserted first,
 * and the no-emit half is driven by a raw event, which does reach the listener in
 * happy-dom (and via a programmatic dispatch in a real browser).
 */
function auditEvent(id: string): AuditEvent {
  return {
    $typeName: 'audit.v1.AuditEvent' as const,
    id,
    resourceType: 'operation',
    resourceId: `operation-${id}`,
    action: 'upgrade',
    status: 'succeeded',
    durationMs: 10n,
    changeSummary: '',
    metadata: {},
  };
}

function mountTable(overrides: Record<string, unknown> = {}) {
  return mount(AuditEventTable, {
    props: {
      events: [auditEvent('e-1')],
      totalSize: 1,
      loading: false,
      hasPrevious: false,
      hasMore: false,
      ...overrides,
    },
  });
}

function pagerButtons(wrapper: ReturnType<typeof mountTable>) {
  return wrapper.get('nav').findAll('button');
}

describe('AuditEventTable pager', () => {
  it('pages from a named navigation landmark using the audit table’s accessible name', () => {
    const wrapper = mountTable();

    // `get('nav')` is the landmark assertion: the region used to be a `<div>` with an
    // aria-label, which is not a landmark any assistive tech announces as one.
    const nav = wrapper.get('nav');
    expect(nav.attributes('aria-label')).toBe(t('audit.table.pagination'));
    // The primitive's fallback ('分页') would be a different landmark name, so this is
    // what pins the page's own label being passed through rather than inherited.
    expect(nav.attributes('aria-label')).not.toBe(t('pagination.label'));
    // The class is the page's only remaining style hook (right alignment); it lands on
    // the primitive's root through attribute fallthrough.
    expect(nav.classes()).toContain('audit-results__pagination');

    const [prev, next] = pagerButtons(wrapper);
    expect(prev!.text()).toBe(t('pagination.previous'));
    expect(next!.text()).toBe(t('pagination.next'));
  });

  it('disables the previous control when the cursor cannot go back, and that control emits nothing', async () => {
    const wrapper = mountTable({ hasPrevious: false, hasMore: true });
    const [prev] = pagerButtons(wrapper);

    // First the state, then the behaviour: asserting only "did not emit" after a
    // `trigger('click')` would be vacuous, because VTU never dispatches to a disabled
    // element.
    expect(prev!.attributes('disabled')).toBeDefined();

    prev!.element.dispatchEvent(new Event('click'));
    await nextTick();

    expect(wrapper.emitted('previous')).toBeUndefined();
  });

  it('emits the public previous/next names exactly once when each direction is reachable', async () => {
    const wrapper = mountTable({ hasPrevious: true, hasMore: true });
    const [prev, next] = pagerButtons(wrapper);

    await prev!.trigger('click');
    await next!.trigger('click');

    // `previous` / `next` are what AuditPage listens for; the primitive's own
    // `prev` / `next` must not leak out under their names.
    expect(wrapper.emitted('previous')).toHaveLength(1);
    expect(wrapper.emitted('next')).toHaveLength(1);
    expect(wrapper.emitted('prev')).toBeUndefined();

    // One click advances the cursor once and carries no payload.
    expect(wrapper.emitted('previous')![0]).toEqual([]);
    expect(wrapper.emitted('next')![0]).toEqual([]);
  });

  it('folds loading into both directions so a page in flight cannot advance the cursor twice', async () => {
    const wrapper = mountTable({ loading: true, hasPrevious: true, hasMore: true });
    const [prev, next] = pagerButtons(wrapper);

    expect(prev!.attributes('disabled')).toBeDefined();
    expect(next!.attributes('disabled')).toBeDefined();

    prev!.element.dispatchEvent(new Event('click'));
    next!.element.dispatchEvent(new Event('click'));
    await nextTick();

    expect(wrapper.emitted('previous')).toBeUndefined();
    expect(wrapper.emitted('next')).toBeUndefined();

    // The same props without the in-flight flag enable both, which is what makes the
    // assertions above about `loading` and not about hasPrevious/hasMore.
    await wrapper.setProps({ loading: false });
    expect(pagerButtons(wrapper)[0]!.attributes('disabled')).toBeUndefined();
    expect(pagerButtons(wrapper)[1]!.attributes('disabled')).toBeUndefined();
  });
});

/*
 * UX-017/UX-018 (W14): the table had no caption (so no accessible name and no
 * table announcement), header cells carried no scope, and a focusable row
 * answered Enter but not Space — while Space is the key screen-reader users are
 * told to press to activate a row.
 */
describe('AuditEventTable accessibility (UX-017/018)', () => {
  it('names the table and binds every header to its column', () => {
    const wrapper = mountTable();
    const caption = wrapper.get('table caption');
    expect(caption.text()).toBe(t('audit.table.results'));
    const headers = wrapper.findAll('thead th');
    expect(headers.length).toBeGreaterThan(0);
    for (const header of headers) expect(header.attributes('scope')).toBe('col');
  });

  it('selects a row with Space, not only Enter', async () => {
    const wrapper = mountTable();
    const row = wrapper.get('tbody tr');
    await row.trigger('keydown', { key: ' ' });
    expect(wrapper.emitted('select')).toHaveLength(1);

    await row.trigger('keydown', { key: 'Enter' });
    expect(wrapper.emitted('select')).toHaveLength(2);
  });
});
