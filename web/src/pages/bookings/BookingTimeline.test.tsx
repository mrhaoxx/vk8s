import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import type { BookingView } from '../../api/types';
import { BookingTimeline } from './BookingTimeline';
import { HOUR_COL, HOUR_MS, NODE_COL } from './timeline';

const base = Date.UTC(2026, 9, 24, 9);
const nodes = [
  { name: 'gpu-07', ready: true, gpu: '8' },
  { name: 'gpu-08', ready: false, gpu: '' },
];
const booking = {
  name: 'nb-1',
  node: 'gpu-07',
  vcluster: 'alice-dev',
  user: 'alice',
  purpose: 'ft',
  start: new Date(base + 2 * HOUR_MS).toISOString(),
  end: new Date(base + 4 * HOUR_MS).toISOString(),
  phase: 'Active',
  reason: '',
  createdAt: '',
  canDelete: true,
} as BookingView;

const cell = (node: string, h: number) => document.querySelector(`.timeline-row[data-node="${node}"] .timeline-cell[data-hour="${h}"]`)!;

describe('BookingTimeline', () => {
  it('renders one row per node and a block per booking', () => {
    render(<BookingTimeline nodes={nodes} bookings={[booking]} baseHourMs={base} nowMs={base + HOUR_MS} me="alice" dark={false} onDraft={() => {}} onSelect={() => {}} />);
    expect(screen.getAllByRole('row')).toHaveLength(2 + 1); // 表头 + 2 行
    const block = screen.getByRole('button', { name: /alice-dev/ });
    expect(block).toHaveClass('is-mine');
    expect(block.style.left).toBe(`${NODE_COL + 2 * HOUR_COL}px`);
  });

  it('drag-selects hours and reports a half-open range', () => {
    const onDraft = vi.fn();
    render(<BookingTimeline nodes={nodes} bookings={[booking]} baseHourMs={base} nowMs={base} me="alice" dark={false} onDraft={onDraft} onSelect={() => {}} />);
    fireEvent.mouseDown(cell('gpu-08', 5), { button: 0 });
    fireEvent.mouseEnter(cell('gpu-08', 7));
    fireEvent.mouseUp(window);
    expect(onDraft).toHaveBeenCalledWith({ node: 'gpu-08', start: new Date(base + 5 * HOUR_MS), end: new Date(base + 8 * HOUR_MS) });
  });

  it('stops the selection before an existing block', () => {
    const onDraft = vi.fn();
    render(<BookingTimeline nodes={nodes} bookings={[booking]} baseHourMs={base} nowMs={base} me="alice" dark={false} onDraft={onDraft} onSelect={() => {}} />);
    fireEvent.mouseDown(cell('gpu-07', 0), { button: 0 });
    fireEvent.mouseEnter(cell('gpu-07', 3)); // 2–3 被 booking 占用
    fireEvent.mouseUp(window);
    expect(onDraft).toHaveBeenCalledWith({ node: 'gpu-07', start: new Date(base), end: new Date(base + 2 * HOUR_MS) });
  });

  it('marks rejected bookings as dead and clicking a block selects it', () => {
    const onSelect = vi.fn();
    const dead = { ...booking, name: 'nb-2', node: 'gpu-08', phase: 'Rejected', reason: 'NodeNotBookable' } as BookingView;
    render(<BookingTimeline nodes={nodes} bookings={[dead]} baseHourMs={base} nowMs={base} me="bob" dark={false} onDraft={() => {}} onSelect={onSelect} />);
    const block = screen.getByRole('button', { name: /alice-dev/ });
    expect(block).toHaveClass('is-dead');
    expect(block).not.toHaveClass('is-mine');
    fireEvent.click(block);
    expect(onSelect).toHaveBeenCalledWith(dead);
  });
});
