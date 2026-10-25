import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it } from 'vitest';
import { ThemeProvider } from '../theme/ThemeContext';
import { Sidebar } from './Sidebar';

const mount = (isAdmin: boolean) =>
  render(
    <MemoryRouter>
      <ThemeProvider>
        <Sidebar
          user={{ name: 'alice', displayName: 'Alice', role: isAdmin ? 'admin' : 'user' }}
          isAdmin={isAdmin}
          open={false}
          onClose={() => {}}
          onLogout={() => {}}
        />
      </ThemeProvider>
    </MemoryRouter>,
  );

describe('Sidebar', () => {
  it('hides the admin group for ordinary users', () => {
    mount(false);
    expect(screen.getByRole('link', { name: '我的集群' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: '节点预约' })).toHaveAttribute('href', '/bookings');
    expect(screen.queryByText('管理')).toBeNull();
    expect(screen.queryByRole('link', { name: '节点' })).toBeNull();
    expect(screen.queryByText('管理员')).toBeNull();
  });

  it('shows the three admin links for admins', () => {
    mount(true);
    expect(screen.getByText('管理')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: '节点预约' })).toBeInTheDocument();
    for (const name of ['用户', '所有集群', '节点']) {
      expect(screen.getByRole('link', { name })).toBeInTheDocument();
    }
    // 用户块上有角色标记（菜单未展开时也可见）
    expect(screen.getByText('管理员')).toBeInTheDocument();
  });
});
