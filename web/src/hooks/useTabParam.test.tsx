import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { describe, expect, it } from 'vitest';
import { useTabParam } from './useTabParam';

const KEYS = ['overview', 'kubeconfig', 'members', 'settings'] as const;
type Key = (typeof KEYS)[number];

function Probe({ allowed }: { allowed: readonly Key[] }) {
  const [tab, setTab] = useTabParam(allowed, 'overview');
  const loc = useLocation();
  return (
    <>
      <span data-testid="tab">{tab}</span>
      <span data-testid="search">{loc.search}</span>
      <button onClick={() => setTab('members')}>members</button>
      <button onClick={() => setTab('overview')}>overview</button>
    </>
  );
}

const mount = (url: string, allowed: readonly Key[] = KEYS) =>
  render(
    <MemoryRouter initialEntries={[url]}>
      <Probe allowed={allowed} />
    </MemoryRouter>,
  );

describe('useTabParam', () => {
  it('defaults to overview without a param', () => {
    mount('/vclusters/a');
    expect(screen.getByTestId('tab')).toHaveTextContent('overview');
  });

  it('reads a valid param', () => {
    mount('/vclusters/a?tab=members');
    expect(screen.getByTestId('tab')).toHaveTextContent('members');
  });

  it('falls back and rewrites an invalid param, keeping other params', () => {
    mount('/vclusters/a?foo=1&tab=bogus');
    expect(screen.getByTestId('tab')).toHaveTextContent('overview');
    expect(screen.getByTestId('search')).toHaveTextContent('?foo=1');
    expect(screen.getByTestId('search')).not.toHaveTextContent('tab=');
  });

  it('falls back when the tab is not allowed for this user', () => {
    mount('/vclusters/a?tab=settings', ['overview', 'kubeconfig', 'members']);
    expect(screen.getByTestId('tab')).toHaveTextContent('overview');
    expect(screen.getByTestId('search')).not.toHaveTextContent('settings');
  });

  it('writes the param on change and removes it for the fallback', async () => {
    mount('/vclusters/a?foo=1');
    await userEvent.click(screen.getByText('members'));
    expect(screen.getByTestId('search')).toHaveTextContent('foo=1');
    expect(screen.getByTestId('search')).toHaveTextContent('tab=members');
    await userEvent.click(screen.getByText('overview'));
    expect(screen.getByTestId('search')).toHaveTextContent('?foo=1');
    expect(screen.getByTestId('search')).not.toHaveTextContent('tab=');
  });
});
