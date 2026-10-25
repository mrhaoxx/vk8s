import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { Tabs, type TabItem } from './Tabs';

const items: TabItem<'a' | 'b' | 'c'>[] = [
  { key: 'a', label: 'A', count: 2 },
  { key: 'b', label: 'B', hidden: true },
  { key: 'c', label: 'C' },
];

describe('Tabs', () => {
  it('renders visible tabs with counts and marks the active one', () => {
    render(<Tabs items={items} active="a" onChange={() => {}} ariaLabel="集群" />);
    expect(screen.getAllByRole('tab')).toHaveLength(2);
    expect(screen.getByRole('tab', { name: /A/ })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('tab', { name: /A/ })).toHaveAttribute('aria-controls', 'panel-a');
    expect(screen.getByText('2')).toHaveClass('tab-count');
    expect(screen.queryByRole('tab', { name: 'B' })).toBeNull();
  });

  it('moves with arrow keys, skipping hidden tabs', async () => {
    const onChange = vi.fn();
    render(<Tabs items={items} active="a" onChange={onChange} ariaLabel="集群" />);
    screen.getByRole('tab', { name: /A/ }).focus();
    await userEvent.keyboard('{ArrowRight}');
    expect(onChange).toHaveBeenLastCalledWith('c');
    await userEvent.keyboard('{ArrowRight}');
    expect(onChange).toHaveBeenLastCalledWith('a');
  });
});
