import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import OperatorStatusBadge from './OperatorStatusBadge.vue';

/*
 * The badge printed the server enum verbatim, so the operator table showed English
 * (Active/Online) while the filters next to it were Chinese.
 */
describe('OperatorStatusBadge', () => {
  it('translates the lifecycle values', () => {
    expect(mount(OperatorStatusBadge, { props: { lifecycleStatus: 'active' } }).text()).toBe('在用');
    expect(mount(OperatorStatusBadge, { props: { lifecycleStatus: 'superseded' } }).text()).toBe('已被取代');
    expect(mount(OperatorStatusBadge, { props: { lifecycleStatus: 'revoked' } }).text()).toBe('已撤销');
  });

  it('translates the session values', () => {
    expect(mount(OperatorStatusBadge, { props: { sessionStatus: 'online' } }).text()).toBe('在线');
    expect(mount(OperatorStatusBadge, { props: { sessionStatus: 'suspect' } }).text()).toBe('可疑');
    expect(mount(OperatorStatusBadge, { props: { sessionStatus: 'offline' } }).text()).toBe('离线');
  });

  // `unknown` is a DECLARED enum value (types/operator.ts), so it is translated rather
  // than treated as a surprise value.
  it('translates the declared unknown status', () => {
    expect(mount(OperatorStatusBadge, { props: { lifecycleStatus: 'unknown' } }).text()).toBe('未知');
    expect(mount(OperatorStatusBadge, { props: { sessionStatus: 'unknown' } }).text()).toBe('未知');
  });

  it('falls back to the raw value for a status it does not know', () => {
    // A new server status must stay visible instead of rendering nothing.
    expect(mount(OperatorStatusBadge, { props: { sessionStatus: 'draining' as never } }).text()).toBe('draining');
  });

  it('labels a missing session', () => {
    expect(mount(OperatorStatusBadge, {}).text()).toBe('无会话');
  });
});
