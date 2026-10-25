import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { ToastProvider } from '../../components/Toast';
import { ThemeProvider } from '../../theme/ThemeContext';
import { AuditView } from './AuditView';

const base = {
  source: 'kube',
  vcluster: 'alice-dev',
  stage: 'ResponseComplete',
  level: 'Request',
  verb: 'delete',
  user: 'alice',
  groups: [],
  impersonatedUser: '',
  sourceIPs: ['10.0.0.1'],
  userAgent: 'kubectl',
  namespace: 'demo',
  apiGroup: 'apps',
  apiVersion: 'v1',
  resource: 'deployments',
  subresource: '',
  name: 'api',
  requestURI: '/apis/apps/v1/namespaces/demo/deployments/api',
  responseCode: 200,
  responseReason: '',
  responseMessage: '',
  platformAction: '',
  platformTarget: '',
  requestTruncated: false,
  annotations: { 'authorization.k8s.io/decision': 'allow' },
};

vi.mock('../../api/audit', () => ({
  listAuditEvents: vi.fn(async () => ({ items: [{ ...base, id: 'e1', ts: '2026-10-24T06:01:58.312Z' }], nextBefore: '' })),
  getAuditEvent: vi.fn(async (id: string) => {
    if (id === 'missing') throw new Error('audit event not found');
    return { ...base, id: 'e1', ts: '2026-10-24T06:01:58.312Z', requestObject: '{"kind":"DeleteOptions"}' };
  }),
}));
vi.mock('../../auth/ConfigContext', () => ({
  useConfig: () => ({ auditEnabled: true, apiDomain: '', appsDomain: '', nodePlacementPresets: [], defaults: {}, limits: {} }),
}));
vi.mock('../../auth/AuthContext', () => ({ useAuth: () => ({ user: { name: 'alice', role: 'user' }, isAdmin: false }) }));

function LocationProbe() {
  const loc = useLocation();
  return <span data-testid="search">{loc.search}</span>;
}

describe('AuditView', () => {
  it('lists events, opens the drawer and syncs ?event=', async () => {
    render(
      <MemoryRouter initialEntries={['/vclusters/alice-dev?tab=audit']}>
        <ThemeProvider>
          <ToastProvider>
            <AuditView fixedVCluster="alice-dev" />
            <LocationProbe />
          </ToastProvider>
        </ThemeProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByText('delete deployments.apps')).toBeInTheDocument();
    expect(screen.queryByRole('columnheader', { name: '集群' })).toBeNull();
    await userEvent.click(screen.getByText('delete deployments.apps'));
    expect(await screen.findByRole('dialog', { name: '事件详情' })).toBeInTheDocument();
    expect(await screen.findByText(/DeleteOptions/)).toBeInTheDocument();
    expect(screen.getByTestId('search').textContent).toContain('event=e1');
    await userEvent.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(screen.getByTestId('search').textContent).not.toContain('event=');
  });

  it('opens the drawer with an error when a deep-linked event cannot be loaded', async () => {
    render(
      <MemoryRouter initialEntries={['/vclusters/alice-dev?tab=audit&event=missing']}>
        <ThemeProvider>
          <ToastProvider>
            <AuditView fixedVCluster="alice-dev" />
          </ToastProvider>
        </ThemeProvider>
      </MemoryRouter>,
    );
    const dialog = await screen.findByRole('dialog', { name: '事件详情' });
    expect(dialog).toBeInTheDocument();
    expect(await screen.findByText(/audit event not found/)).toBeInTheDocument();
  });
});
