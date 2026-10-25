import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { StatusDot, statusKind } from './StatusDot';

describe('statusKind', () => {
  it.each([
    ['Running', 'ok'],
    ['True', 'ok'],
    ['Ready', 'ok'],
    ['Pending', 'busy'],
    ['Provisioning', 'busy'],
    ['Failed', 'err'],
    ['False', 'err'],
    ['Deleting', 'idle'],
    ['Unknown', 'idle'],
    ['', 'idle'],
    ['whatever', 'idle'],
  ])('maps %s to %s', (value, kind) => {
    expect(statusKind(value)).toBe(kind);
  });
});

describe('StatusDot', () => {
  it('shows 未知 for an empty phase and carries the kind class', () => {
    const { container } = render(<StatusDot value="" />);
    expect(screen.getByText('未知')).toBeInTheDocument();
    expect(container.querySelector('.status')).toHaveClass('status-idle');
  });

  it('prefers an explicit label and exposes title', () => {
    render(<StatusDot value="Running" label="运行中" title="all good" />);
    expect(screen.getByText('运行中')).toBeInTheDocument();
    expect(screen.getByTitle('all good')).toBeInTheDocument();
  });
});
