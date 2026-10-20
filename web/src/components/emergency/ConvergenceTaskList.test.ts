import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import ConvergenceTaskList from './ConvergenceTaskList.vue';
import type { ConvergenceTaskDisplay } from '@/features/emergency/model';

/*
 * TASK-334 ②: the task list used to be suspected of rendering a task without an
 * active revision as "草稿" (`statusLabel('valuesRevision', task.activeRevisionStatus
 * || 'draft')`). It does not, and the reason is structural, not accidental: the
 * bound branch is guarded by `v-if="task.activeRevisionId"`, so the fallback only
 * runs when a revision id exists. Every write path that binds a revision sets
 * `active_revision_id` and `active_revision_status` together
 * (internal/store/{sqlite,postgres}/values_lifecycle.go binds both; the approval
 * and rejection paths clear both), so an id without a status is not reachable
 * through the application.
 *
 * These cases pin that reachability boundary rather than the fallback itself: a row
 * without an active revision must never claim a draft, and the positive control
 * keeps the real draft label honest. If a later change moves the bound branch off
 * `activeRevisionId`, the first case fails instead of silently resurrecting the
 * misleading label.
 */
function task(overrides: Partial<ConvergenceTaskDisplay> = {}): ConvergenceTaskDisplay {
  return {
    taskId: 'task-1',
    operationId: 'op-1',
    opType: 'SET_CONTAINER_IMAGE',
    targetSummary: 'deployments/default/app',
    submittedAt: null,
    reasonDisplay: 'emergency change',
    promotionPaths: ['image.tag'],
    activeRevisionId: '',
    activeRevisionStatus: '',
    selectable: true,
    incompatibilityReason: '',
    ...overrides,
  };
}

function mountList(tasks: ConvergenceTaskDisplay[]) {
  return mount(ConvergenceTaskList, { props: { tasks, selectedTaskIds: [] } });
}

describe('ConvergenceTaskList revision status labelling', () => {
  it('does not render a task without an active revision as a draft', () => {
    const wrapper = mountList([task()]);

    expect(wrapper.text()).toContain('待提升');
    expect(wrapper.text()).not.toContain('草稿');
    expect(wrapper.text()).not.toContain('已绑定');
  });

  it('labels an unselectable task without an active revision by its reason', () => {
    const wrapper = mountList([task({ selectable: false, incompatibilityReason: '任务已收敛' })]);

    expect(wrapper.text()).toContain('任务已收敛');
    expect(wrapper.text()).not.toContain('草稿');
  });

  // Positive control: a real bound draft is still shown as a draft.
  it('still labels a bound draft as a draft', () => {
    const wrapper = mountList([task({ activeRevisionId: 'rev-1', activeRevisionStatus: 'draft' })]);

    expect(wrapper.text()).toContain('已绑定');
    expect(wrapper.text()).toContain('草稿');
  });
});

/*
 * TASK-394: the list was the one table on the convergence page with no frame (rows floated on
 * the page background) and its header painted `--color-bg`, which is the page background
 * rather than a header surface. The structure below is what the page relies on: one scroll
 * region, framed, with the header inside it.
 */
describe('ConvergenceTaskList direction-C frame (TASK-394)', () => {
  it('keeps the header inside a framed scroll region', () => {
    const wrapper = mountList([task()]);

    const frame = wrapper.get('.task-table-wrap');
    expect(frame.classes()).toContain('table-wrap');
    expect(frame.attributes('role')).toBe('region');
    expect(frame.find('table.task-table').exists()).toBe(true);
    expect(wrapper.findAll('thead th')).toHaveLength(6);
  });
});
