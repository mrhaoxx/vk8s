import { describe, expect, it } from 'vitest';
import { tabItems, visibleTabs } from './tabs';

describe('visibleTabs', () => {
  it('shows settings to owners and admins only', () => {
    expect(visibleTabs('owner', false)).toEqual(['overview', 'kubeconfig', 'members', 'audit', 'settings']);
    expect(visibleTabs('member', true)).toEqual(['overview', 'kubeconfig', 'members', 'audit', 'settings']);
    expect(visibleTabs('member', false)).toEqual(['overview', 'kubeconfig', 'members', 'audit']);
    expect(visibleTabs('', false)).toEqual(['overview', 'kubeconfig', 'members', 'audit']);
  });
});

describe('tabItems', () => {
  it('omits the token count while tokens are loading', () => {
    const items = tabItems(['overview', 'kubeconfig', 'members'], { tokens: undefined, members: 2 });
    expect(items.find((i) => i.key === 'kubeconfig')?.count).toBeUndefined();
    expect(items.find((i) => i.key === 'members')?.count).toBe(2);
  });

  it('shows the token count once loaded', () => {
    const items = tabItems(['overview', 'kubeconfig'], { tokens: 3, members: 0 });
    expect(items.find((i) => i.key === 'kubeconfig')?.count).toBe(3);
  });
});

describe('audit tab', () => {
  it('is visible to every role and sits before settings', () => {
    expect(visibleTabs('member', false)).toContain('audit');
    const owner = visibleTabs('owner', false);
    expect(owner.indexOf('audit')).toBeLessThan(owner.indexOf('settings'));
    expect(tabItems(['audit'], { tokens: 1, members: 1 })[0]).toEqual({ key: 'audit', label: '审计' });
  });
});

describe('apps tab', () => {
  it('includes apps between members and audit only when addons are enabled', () => {
    expect(visibleTabs('member', false, true)).toEqual(['overview', 'kubeconfig', 'members', 'apps', 'audit']);
    expect(visibleTabs('member', false, false)).not.toContain('apps');
    expect(visibleTabs('member', false)).not.toContain('apps');
  });
});
