import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { AddonInstallView, AddonTemplateView, VClusterView } from '../../api/types';
import { ToastProvider } from '../../components/Toast';
import { ThemeProvider } from '../../theme/ThemeContext';
import { AppsTab } from './AppsTab';

const catalog: AddonTemplateView[] = [
  { name: 'grafana', displayName: 'Grafana', description: '看板', version: '10.5.15', inputs: [], requires: {} },
  { name: 'devpods', displayName: 'DevPods', description: 'ssh 开发环境', version: '0.1.0', inputs: [], requires: { podSecurity: 'privileged' } },
  { name: 'prometheus', displayName: 'Prometheus', description: '指标', version: '29.38.0', inputs: [], requires: {} },
];
let installed: AddonInstallView[] = [];

vi.mock('../../api/addons', () => ({
  listAddonCatalog: vi.fn(async () => catalog),
  listClusterAddons: vi.fn(async () => installed),
  installAddon: vi.fn(async () => ({})),
  uninstallAddon: vi.fn(async () => undefined),
  upgradeAddon: vi.fn(async () => undefined),
  retryAddon: vi.fn(async () => undefined),
  getAddonOutputs: vi.fn(async () => [{ name: 'pw', label: '密码', value: 'pw-123' }]),
  getAddonLogs: vi.fn(async () => ({ log: 'Error: boom' })),
}));

const base: AddonInstallView = {
  template: 'grafana', displayName: 'Grafana', phase: 'Ready', installedVersion: '10.5.15', inputs: {},
  outputs: [{ name: 'web', label: 'Web', value: '172.28.0.9:80' }], hasSecretOutputs: true, templateRemoved: false, deleting: false, createdAt: '2026-10-25T00:00:00Z',
};

function vc(role: 'owner' | 'member', podSecurity = 'baseline'): VClusterView {
  return { name: 'alice-dev', owner: 'alice', members: ['bob'], myRole: role, podSecurity, status: { phase: 'Running' } } as unknown as VClusterView;
}

function renderTab(v: VClusterView) {
  return render(
    <MemoryRouter>
      <ThemeProvider>
        <ToastProvider>
          <AppsTab vc={v} isAdmin={false} />
        </ToastProvider>
      </ThemeProvider>
    </MemoryRouter>,
  );
}

describe('AppsTab', () => {
  beforeEach(() => {
    installed = [];
  });

  it('shows Ready card with outputs and copy button, and upgrade badge when availableVersion is set', async () => {
    installed = [{ ...base, availableVersion: '10.6.0' }];
    renderTab(vc('owner'));
    expect(await screen.findByText('172.28.0.9:80')).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: '复制' }).length).toBeGreaterThan(0);
    expect(screen.getByText(/有新版本 10\.6\.0/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '升级' })).toBeInTheDocument();
  });

  it('shows Failed with reason and a 查看日志 action that loads logs', async () => {
    installed = [{ ...base, phase: 'Failed', reason: 'JobFailed', message: 'helm failed', outputs: [] }];
    renderTab(vc('owner'));
    expect(await screen.findByText(/helm failed/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '查看日志' }));
    expect(await screen.findByText(/Error: boom/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '重试' })).toBeInTheDocument();
  });

  it('hides install / uninstall / retry / upgrade for members', async () => {
    installed = [{ ...base, availableVersion: '10.6.0' }];
    renderTab(vc('member'));
    await screen.findByText('172.28.0.9:80');
    for (const name of ['安装', '卸载', '重试', '升级']) {
      expect(screen.queryByRole('button', { name })).toBeNull();
    }
  });

  it('disables install for templates whose requires are unmet and shows the reason', async () => {
    renderTab(vc('owner', 'baseline'));
    await screen.findByText('DevPods');
    const buttons = screen.getAllByRole('button', { name: '安装' });
    const devpodsCard = screen.getByText('DevPods').closest('[data-addon]') as HTMLElement;
    const btn = devpodsCard.querySelector('button') as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
    expect(devpodsCard.textContent).toContain('需要 privileged 档位');
    expect(buttons.some((b) => !(b as HTMLButtonElement).disabled)).toBe(true);
  });

  it('reveals secret outputs on 显示 for owners', async () => {
    installed = [base];
    renderTab(vc('owner'));
    await screen.findByText('172.28.0.9:80');
    fireEvent.click(screen.getByRole('button', { name: '显示' }));
    await waitFor(() => expect(screen.getByText('pw-123')).toBeInTheDocument());
  });
});

describe('AppsTab secret-only outputs', () => {
  it('shows revealed secrets even when there are no plain outputs', async () => {
    installed = [{ ...base, outputs: [] }];
    renderTab(vc('owner'));
    fireEvent.click(await screen.findByRole('button', { name: '显示' }));
    await waitFor(() => expect(screen.getByText('pw-123')).toBeInTheDocument());
  });
});

describe('AppsTab values-only update', () => {
  it('offers an upgrade when the catalog changed without a version bump', async () => {
    installed = [{ ...base, updateAvailable: true }];
    renderTab(vc('owner'));
    expect(await screen.findByText('有更新')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '升级' })).toBeInTheDocument();
  });
});
