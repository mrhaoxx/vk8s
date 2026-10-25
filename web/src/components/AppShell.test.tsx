import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { ThemeProvider } from '../theme/ThemeContext';
import { AppShell } from './AppShell';

vi.mock('../auth/AuthContext', () => ({
  useAuth: () => ({
    user: { name: 'root', displayName: 'Root', email: '', role: 'admin' },
    isAdmin: true,
    logout: vi.fn(),
  }),
}));

describe('AppShell', () => {
  it('closes the drawer when the route changes', async () => {
    render(
      <MemoryRouter initialEntries={['/']}>
        <ThemeProvider>
          <Routes>
            <Route element={<AppShell />}>
              <Route path="/" element={<p>home</p>} />
              <Route path="/admin/nodes" element={<p>nodes</p>} />
            </Route>
          </Routes>
        </ThemeProvider>
      </MemoryRouter>,
    );
    await userEvent.click(screen.getByRole('button', { name: '打开导航' }));
    expect(screen.getByRole('complementary', { name: '主导航' })).toHaveClass('sidebar-open');
    await userEvent.click(screen.getByRole('link', { name: '节点' }));
    expect(screen.getByText('nodes')).toBeInTheDocument();
    expect(screen.getByRole('complementary', { name: '主导航' })).not.toHaveClass('sidebar-open');
  });

  it('closes the drawer when the link for the current route is clicked', async () => {
    render(
      <MemoryRouter initialEntries={['/']}>
        <ThemeProvider>
          <Routes>
            <Route element={<AppShell />}>
              <Route path="/" element={<p>home</p>} />
            </Route>
          </Routes>
        </ThemeProvider>
      </MemoryRouter>,
    );
    await userEvent.click(screen.getByRole('button', { name: '打开导航' }));
    expect(screen.getByRole('complementary', { name: '主导航' })).toHaveClass('sidebar-open');
    await userEvent.click(screen.getByRole('link', { name: '我的集群' }));
    expect(screen.getByRole('complementary', { name: '主导航' })).not.toHaveClass('sidebar-open');
  });
});
