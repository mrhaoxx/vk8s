import { useState } from 'react';
import { listBookings } from '../api/bookings';
import type { BookingView } from '../api/types';
import { listVClusters } from '../api/vclusters';
import { useAuth } from '../auth/AuthContext';
import { Button } from '../components/Button';
import { PageHeader } from '../components/PageHeader';
import { EmptyState, ErrorState, Loading } from '../components/State';
import { useAsync } from '../hooks/useAsync';
import { useNow } from '../hooks/useNow';
import { usePolling } from '../hooks/usePolling';
import { useIsDark } from '../theme/ThemeContext';
import { BookingDetailDialog, CreateBookingDialog } from './bookings/BookingDialogs';
import { BookingTimeline, type BookingDraft } from './bookings/BookingTimeline';
import { HOUR_MS, OCCUPYING, dayLabel, floorHour, hourLabel, windowFor } from './bookings/timeline';

const DAY_MS = 24 * HOUR_MS;

export function BookingsPage() {
  const { user, isAdmin } = useAuth();
  const dark = useIsDark();
  const nowMs = useNow(60_000);
  const [baseHourMs, setBaseHourMs] = useState(() => floorHour(Date.now()));
  const [draft, setDraft] = useState<BookingDraft | null>(null);
  const [selected, setSelected] = useState<BookingView | null>(null);

  const data = useAsync(() => {
    const { from, to } = windowFor(baseHourMs);
    return listBookings(from, to);
  }, [baseHourMs]);
  const clusters = useAsync(listVClusters, []);

  const bookings = data.data?.bookings ?? [];
  const nodes = data.data?.nodes ?? [];
  usePolling(
    bookings.some((b) => OCCUPYING.has(b.phase)),
    30_000,
    data.reload,
  );

  const myClusters = (clusters.data ?? []).filter((vc) => (isAdmin || vc.myRole === 'owner' || vc.myRole === 'member') && vc.status.phase !== 'Deleting');

  return (
    <>
      <PageHeader
        title="节点预约"
        subtitle={data.data ? `${nodes.length} 个可预约节点 · 拖选小时格创建预约 · ${dayLabel(baseHourMs)} ${hourLabel(baseHourMs)}:00 起 48 小时` : undefined}
        actions={
          <>
            <Button onClick={() => setBaseHourMs((b) => b - DAY_MS)}>‹ 前 24h</Button>
            <Button onClick={() => setBaseHourMs(floorHour(Date.now()))}>今天</Button>
            <Button onClick={() => setBaseHourMs((b) => b + DAY_MS)}>后 24h ›</Button>
            <Button onClick={data.reload} disabled={data.loading}>
              刷新
            </Button>
          </>
        }
      />

      {data.loading && !data.data ? (
        <Loading />
      ) : data.error && !data.data ? (
        <ErrorState message={data.error} onRetry={data.reload} />
      ) : nodes.length === 0 ? (
        <EmptyState title="还没有可预约节点" hint="请联系管理员在「节点」页把节点标记为可预约。" />
      ) : (
        <div className="timeline-scroll">
          <BookingTimeline nodes={nodes} bookings={bookings} baseHourMs={baseHourMs} nowMs={nowMs} me={user?.name ?? ''} dark={dark} onDraft={setDraft} onSelect={setSelected} />
        </div>
      )}

      {draft && (
        <CreateBookingDialog
          draft={draft}
          clusters={myClusters}
          onClose={() => setDraft(null)}
          onCreated={() => {
            setDraft(null);
            data.reload();
          }}
        />
      )}
      {selected && (
        <BookingDetailDialog
          booking={selected}
          nowMs={nowMs}
          onClose={() => setSelected(null)}
          onDeleted={() => {
            setSelected(null);
            data.reload();
          }}
        />
      )}
    </>
  );
}
