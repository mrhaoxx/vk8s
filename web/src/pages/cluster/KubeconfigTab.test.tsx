import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it } from 'vitest';
import type { TokenView, VClusterView } from '../../api/types';
import { ToastProvider } from '../../components/Toast';
import { ThemeProvider } from '../../theme/ThemeContext';
import { KubeconfigTab } from './KubeconfigTab';

const vc = { name: 'alice-dev', owner: 'alice', members: [], status: { phase: 'Running' } } as unknown as VClusterView;
const tokens: TokenView[] = [{ id: 'tok-3fa9c2e1b07d4a56', user: 'alice', description: 'macbook', createdAt: '2026-10-21T00:00:00Z', expiresAt: '2026-11-20T00:00:00Z' }];

function renderTab(auditEnabled: boolean) {
  return render(
    <MemoryRouter initialEntries={['/vclusters/alice-dev?tab=kubeconfig']}>
      <ThemeProvider>
        <ToastProvider>
          <KubeconfigTab vc={vc} tokens={{ data: tokens, error: null, loading: false, reload: () => {} }} onRevoked={() => {}} auditEnabled={auditEnabled} />
        </ToastProvider>
      </ThemeProvider>
    </MemoryRouter>,
  );
}

describe('KubeconfigTab', () => {
  it('links each token to the audit tab filtered by that token when audit is enabled', () => {
    renderTab(true);
    const link = screen.getByRole('link', { name: '审计' });
    expect(link.getAttribute('href')).toContain('tab=audit');
    expect(link.getAttribute('href')).toContain('token=tok-3fa9c2e1b07d4a56');
  });

  it('has no audit link when audit is disabled', () => {
    renderTab(false);
    expect(screen.queryByRole('link', { name: '审计' })).toBeNull();
  });
});
