import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ThemeProvider } from '../theme/ThemeContext';
import { UserMenu } from './UserMenu';

afterEach(() => {
  localStorage.clear();
  delete document.documentElement.dataset.theme;
});

describe('UserMenu', () => {
  it('opens, switches theme, logs out and closes on Escape', async () => {
    const onLogout = vi.fn();
    render(
      <ThemeProvider>
        <UserMenu user={{ name: 'alice', displayName: 'Alice', role: 'user' }} onLogout={onLogout} />
      </ThemeProvider>,
    );
    await userEvent.click(screen.getByRole('button', { name: /Alice/ }));
    expect(screen.getByRole('menu')).toBeInTheDocument();
    await userEvent.click(screen.getByLabelText('深色'));
    expect(document.documentElement.dataset.theme).toBe('dark');
    await userEvent.click(screen.getByRole('menuitem', { name: '退出登录' }));
    expect(onLogout).toHaveBeenCalledTimes(1);
    await userEvent.click(screen.getByRole('button', { name: /Alice/ }));
    expect(screen.getByRole('menu')).toBeInTheDocument();
    await userEvent.keyboard('{Escape}');
    expect(screen.queryByRole('menu')).toBeNull();
  });
});
