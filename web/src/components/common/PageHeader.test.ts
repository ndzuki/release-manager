import { RouterLinkStub, mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import PageHeader from './PageHeader.vue';

/*
 * PageHeader contract (TASK-394). The direction-C spec is explicit about one failure
 * mode: a breadcrumb must carry NAMES, never an identifier. The component cannot enforce
 * what the caller passes, so the assertions pin the structural half — earlier crumbs are
 * links, the last crumb is the current page and never a link (even when `to` is given),
 * and the heading is a single <h1>.
 */
const crumbs = [
  { label: '客户', to: { name: 'CustomerList' } },
  { label: '集群', to: { name: 'ClusterList' } },
  { label: '发布清单' },
];

function mountHeader(props: Record<string, unknown> = {}) {
  return mount(PageHeader, {
    props: { title: '发布操作', ...props },
    global: { stubs: { RouterLink: RouterLinkStub } },
  });
}

describe('PageHeader', () => {
  it('renders the category eyebrow above the title when supplied', async () => {
    const wrapper = mount(PageHeader, {
      props: { title: '发布清单', eyebrow: '发布' },
      global: { stubs: { RouterLink: true } },
    });
    const eyebrow = wrapper.get('.page-header__eyebrow');
    expect(eyebrow.text()).toBe('发布');
    // Above the title in DOM order: the category reads before the page name.
    expect(eyebrow.element.nextElementSibling?.tagName).toBe('H1');
  });

  it('omits the eyebrow when not supplied', () => {
    const wrapper = mount(PageHeader, {
      props: { title: '发布清单' },
      global: { stubs: { RouterLink: true } },
    });
    expect(wrapper.find('.page-header__eyebrow').exists()).toBe(false);
  });

  it('renders the page title as the single h1', () => {
    const wrapper = mountHeader();

    expect(wrapper.findAll('h1')).toHaveLength(1);
    expect(wrapper.get('h1').text()).toBe('发布操作');
  });

  it('links earlier crumbs and marks the last one as the current page', () => {
    const wrapper = mountHeader({ crumbs });

    const links = wrapper.findAllComponents(RouterLinkStub);
    expect(links.map((link) => link.text())).toEqual(['客户', '集群']);

    const current = wrapper.findAll('[aria-current="page"]');
    expect(current).toHaveLength(1);
    expect(current[0]!.text()).toBe('发布清单');
  });

  it('never links the last crumb even when it carries a target', () => {
    const wrapper = mountHeader({
      crumbs: [{ label: '客户', to: { name: 'CustomerList' } }, { label: '当前页', to: { name: 'Home' } }],
    });

    const links = wrapper.findAllComponents(RouterLinkStub);
    expect(links).toHaveLength(1);
    expect(links[0]!.text()).toBe('客户');
    expect(wrapper.get('[aria-current="page"]').text()).toBe('当前页');
  });

  it('names the breadcrumb landmark from the catalog', () => {
    const wrapper = mountHeader({ crumbs });

    expect(wrapper.get('nav').attributes('aria-label')).toBe('面包屑');
  });

  it('omits the breadcrumb navigation when no crumbs were passed', () => {
    const wrapper = mountHeader();

    expect(wrapper.find('nav').exists()).toBe(false);
  });

  it('renders the subtitle and the actions slot only when supplied', () => {
    const wrapper = mountHeader({ subtitle: '选择制品和已审批配置' });
    expect(wrapper.get('.page-header__subtitle').text()).toBe('选择制品和已审批配置');
    expect(wrapper.find('.page-header__actions').exists()).toBe(false);

    const withActions = mount(PageHeader, {
      props: { title: '发布操作' },
      slots: { actions: '<button type="button">创建</button>' },
      global: { stubs: { RouterLink: RouterLinkStub } },
    });
    expect(withActions.get('.page-header__actions').text()).toBe('创建');
  });
});
