import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { ToastProvider } from '../components/Toast';
import { ThemeProvider } from '../theme/ThemeContext';
import { ClusterDetailPage } from './ClusterDetailPage';

const { vc } = vi.hoisted(() => ({
  vc: {
    name: 'alice-dev',
    owner: 'alice',
    members: ['bob'],
    quota: { cpu: '16', memory: '64Gi', pods: 50, storage: '200Gi', services: 20, extended: {} },
    nodePlacement: { nodeSelector: {}, nodeNames: [], tolerations: [] },
    podSecurity: 'privileged',
    controlPlane: { storageSize: '5Gi', storageClassName: '' },
    status: { phase: 'Running', reason: '', hostNamespace: 'vc-alice-dev', apiEndpoint: 'https://alice-dev.vk8s.corp.com', appsDomain: 'alice-dev.apps.vk8s.corp.com', conditions: [] },
    createdAt: new Date().toISOString(),
    myRole: 'member',
  },
}));

// bob 是成员：看不到「设置」Tab。
vi.mock('../auth/AuthContext', () => ({
  useAuth: () => ({ user: { name: 'bob', displayName: 'Bob', email: '', role: 'user' }, isAdmin: false, logout: vi.fn() }),
}));
vi.mock('../auth/ConfigContext', () => ({ useConfig: () => null }));
vi.mock('../api/vclusters', () => ({ getVCluster: vi.fn(async () => vc) }));
vi.mock('../api/tokens', () => ({ listTokens: vi.fn(async () => []) }));

function SearchProbe() {
  const loc = useLocation();
  return <span data-testid="search">{loc.search}</span>;
}

describe('ClusterDetailPage', () => {
  it('falls back to overview and strips ?tab=settings for a member once the cluster loads', async () => {
    render(
      <MemoryRouter initialEntries={['/vclusters/alice-dev?tab=settings']}>
        <ThemeProvider>
          <ToastProvider>
            <Routes>
              <Route
                path="/vclusters/:name"
                element={
                  <>
                    <ClusterDetailPage />
                    <SearchProbe />
                  </>
                }
              />
            </Routes>
          </ToastProvider>
        </ThemeProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByText('基本信息')).toBeInTheDocument();
    expect(screen.queryByRole('tab', { name: /设置/ })).toBeNull();
    await waitFor(() => expect(screen.getByTestId('search').textContent).toBe(''));
    // 内容区是 tabpanel，并与当前 tab 关联。
    expect(screen.getByRole('tabpanel')).toHaveAttribute('aria-labelledby', 'tab-overview');
  });
});
