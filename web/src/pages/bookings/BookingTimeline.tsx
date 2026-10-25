import { useEffect, useMemo, useState } from 'react';
import type { BookableNodeView, BookingView } from '../../api/types';
import { StatusDot } from '../../components/StatusDot';
import { clusterColor } from './colors';
import { HOUR_COL, HOUR_MS, HOURS_VISIBLE, NODE_COL, ROW_H, blockGeometry, clampHover, dayLabel, formatRange, hourLabel, isLocalDayStart, occupiedHours, rangeToTimes, selection } from './timeline';

export interface BookingDraft {
  node: string;
  start: Date;
  end: Date;
}

interface Props {
  nodes: BookableNodeView[];
  bookings: BookingView[];
  baseHourMs: number;
  nowMs: number;
  // 当前用户名：自己创建的预约加描边。
  me: string;
  dark: boolean;
  onDraft: (d: BookingDraft) => void;
  onSelect: (b: BookingView) => void;
}

interface Drag {
  node: string;
  anchor: number;
  hover: number;
}

const DEAD = new Set(['Rejected', 'Revoked']);

// 时间轴：每个可预约节点一行，48 个小时格为列。在空格上按下左键横向拖动、松开 → onDraft；点色块 → onSelect。
export function BookingTimeline({ nodes, bookings, baseHourMs, nowMs, me, dark, onDraft, onSelect }: Props) {
  const [drag, setDrag] = useState<Drag | null>(null);
  const hours = useMemo(() => Array.from({ length: HOURS_VISIBLE }, (_, i) => i), []);
  // 当前整点仍可选，之前的不行。
  const firstSelectable = Math.max(0, Math.floor((nowMs - baseHourMs) / HOUR_MS));

  const byNode = useMemo(() => {
    const m = new Map<string, BookingView[]>();
    for (const b of bookings) {
      const arr = m.get(b.node) ?? [];
      arr.push(b);
      m.set(b.node, arr);
    }
    return m;
  }, [bookings]);
  const occupiedByNode = useMemo(() => {
    const m = new Map<string, Set<number>>();
    for (const n of nodes) m.set(n.name, occupiedHours(bookings, n.name, baseHourMs));
    return m;
  }, [nodes, bookings, baseHourMs]);

  // 松开鼠标（不限于格子内）结束拖选。
  useEffect(() => {
    if (!drag) return;
    const up = () => {
      const [lo, hi] = selection(drag.anchor, drag.hover);
      const { start, end } = rangeToTimes(baseHourMs, lo, hi);
      setDrag(null);
      onDraft({ node: drag.node, start, end });
    };
    window.addEventListener('mouseup', up);
    return () => window.removeEventListener('mouseup', up);
  }, [drag, baseHourMs, onDraft]);

  const nowOffset = ((nowMs - baseHourMs) / HOUR_MS) * HOUR_COL;
  const width = NODE_COL + HOURS_VISIBLE * HOUR_COL;

  return (
    <div className="timeline" role="grid" aria-label="节点预约时间轴" style={{ width }}>
      <div className="timeline-head" role="row">
        <div className="timeline-node timeline-corner" role="columnheader" style={{ width: NODE_COL }}>
          节点
        </div>
        {hours.map((h) => {
          const ms = baseHourMs + h * HOUR_MS;
          const day = isLocalDayStart(ms);
          return (
            <div key={h} role="columnheader" className={day ? 'timeline-hour is-day' : 'timeline-hour'} style={{ width: HOUR_COL }} title={new Date(ms).toLocaleString()}>
              {hourLabel(ms)}
              {day && <span>{dayLabel(ms)}</span>}
            </div>
          );
        })}
      </div>

      {nodes.map((n) => {
        const occupied = occupiedByNode.get(n.name) ?? new Set<number>();
        const active = drag && drag.node === n.name ? drag : null;
        const [selLo, selHi] = active ? selection(active.anchor, active.hover) : [-1, -1];
        return (
          <div key={n.name} className="timeline-row" role="row" data-node={n.name} style={{ minHeight: ROW_H }}>
            <div className="timeline-node" role="rowheader" style={{ width: NODE_COL }} title={n.ready ? n.name : `${n.name} · NotReady`}>
              <span className={n.ready ? 'mono strong' : 'mono strong muted'}>{n.name}</span>
              <span className="timeline-node-meta">{n.ready ? (n.gpu ? `${n.gpu}×GPU` : '') : <StatusDot value="False" label="NotReady" />}</span>
            </div>
            {hours.map((h) => {
              const past = h < firstSelectable;
              const occ = occupied.has(h);
              const selectable = !past && !occ;
              const inSel = h >= selLo && h <= selHi;
              const cls = ['timeline-cell', past && 'is-past', occ && 'is-occupied', inSel && 'is-selected', inSel && h === selLo && 'is-sel-first', inSel && h === selHi && 'is-sel-last']
                .filter(Boolean)
                .join(' ');
              return (
                <div
                  key={h}
                  role="gridcell"
                  className={cls}
                  data-hour={h}
                  data-selectable={selectable ? 'true' : 'false'}
                  style={{ width: HOUR_COL }}
                  onMouseDown={(e) => {
                    if (e.button !== 0 || !selectable) return;
                    e.preventDefault();
                    setDrag({ node: n.name, anchor: h, hover: h });
                  }}
                  onMouseEnter={() => setDrag((d) => (d && d.node === n.name ? { ...d, hover: clampHover(d.anchor, h, occupied, firstSelectable) } : d))}
                />
              );
            })}
            {active && (
              <span className="timeline-sel-label" style={{ left: NODE_COL + selLo * HOUR_COL }}>
                {formatRange(baseHourMs + selLo * HOUR_MS, baseHourMs + (selHi + 1) * HOUR_MS)} · {selHi - selLo + 1}h
              </span>
            )}
            {(byNode.get(n.name) ?? []).map((b) => {
              const g = blockGeometry(Date.parse(b.start), Date.parse(b.end), baseHourMs);
              if (!g) return null;
              const dead = DEAD.has(b.phase);
              const cls = ['timeline-block', b.user === me && !dead && 'is-mine', dead && 'is-dead', g.clipLeft && 'clip-l', g.clipRight && 'clip-r'].filter(Boolean).join(' ');
              const title = `${b.vcluster} · ${b.user}${b.purpose ? ` · ${b.purpose}` : ''}\n${formatRange(Date.parse(b.start), Date.parse(b.end))}${dead ? `\n${b.phase}: ${b.reason}` : ''}`;
              return (
                <button
                  key={b.name}
                  type="button"
                  className={cls}
                  style={{ left: g.left, width: g.width, background: dead ? undefined : clusterColor(b.vcluster, dark) }}
                  title={title}
                  onMouseDown={(e) => e.stopPropagation()}
                  onClick={() => onSelect(b)}
                >
                  <b className="mono">{b.vcluster}</b>
                  <span className="timeline-block-meta">
                    · {b.user}
                    {b.purpose && ` · ${b.purpose}`}
                  </span>
                </button>
              );
            })}
          </div>
        );
      })}

      {nowOffset >= 0 && nowOffset <= HOURS_VISIBLE * HOUR_COL && (
        <div className="timeline-now" style={{ left: NODE_COL + nowOffset }} title={`现在：${new Date(nowMs).toLocaleTimeString()}`} aria-hidden="true" />
      )}
    </div>
  );
}
