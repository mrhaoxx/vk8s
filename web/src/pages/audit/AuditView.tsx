import { useCallback, useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { getAuditEvent, listAuditEvents } from '../../api/audit';
import { errorMessage } from '../../api/client';
import type { AuditEventView } from '../../api/types';
import { listVClusters } from '../../api/vclusters';
import { useAuth } from '../../auth/AuthContext';
import { useConfig } from '../../auth/ConfigContext';
import { Badge } from '../../components/Badge';
import { Button } from '../../components/Button';
import { Card } from '../../components/Card';
import { EmptyState, ErrorState, Loading } from '../../components/State';
import { StatusDot } from '../../components/StatusDot';
import { Table } from '../../components/Table';
import { useAsync } from '../../hooks/useAsync';
import { useNow } from '../../hooks/useNow';
import { usePolling } from '../../hooks/usePolling';
import { AuditDrawer } from './AuditDrawer';
import { AuditFilters } from './AuditFilters';
import { FILTER_KEYS, cursorOf, formatEventTime, formatEventTimeFull, mergePage, parseFilters, rangeFor, serializeFilters, type AuditFilterState } from './filters';
import { SOURCE_LABELS, actionLabel, objectLabel, outcomeKind } from './labels';

const PAGE = 100;

interface Props {
  // 集群详情里使用：集群固定，不显示集群过滤与集群列
  fixedVCluster?: string;
}

// 审计列表 + 过滤 + 抽屉；过滤与选中事件都同步在 URL 里（replace），其他查询参数（如 tab）保留。
export function AuditView({ fixedVCluster }: Props) {
  const config = useConfig();
  const { isAdmin } = useAuth();
  const [sp, setSp] = useSearchParams();
  const filters = useMemo(() => parseFilters(sp), [sp]);
  const eventID = sp.get('event') ?? '';
  const vcluster = fixedVCluster ?? filters.vcluster;

  const writeParams = useCallback(
    (mutate: (next: URLSearchParams) => void) => {
      setSp(
        (prev) => {
          const next = new URLSearchParams(prev);
          mutate(next);
          return next;
        },
        { replace: true },
      );
    },
    [setSp],
  );
  const setFilters = (f: AuditFilterState) =>
    writeParams((next) => {
      for (const k of FILTER_KEYS) next.delete(k);
      serializeFilters(f).forEach((v, k) => next.set(k, v));
      next.delete('event');
    });
  const selectEvent = useCallback((id: string) => writeParams((next) => (id ? next.set('event', id) : next.delete('event'))), [writeParams]);

  const clusters = useAsync(async () => (fixedVCluster ? [] : (await listVClusters()).map((v) => v.name)), [fixedVCluster]);

  const [items, setItems] = useState<AuditEventView[]>([]);
  const [nextBefore, setNextBefore] = useState('');
  const [loading, setLoading] = useState(false);
  const [more, setMore] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [auto, setAuto] = useState(false);
  const now = useNow(60_000);
  const filterKey = JSON.stringify({ ...filters, vcluster });

  const query = useCallback(
    (before?: string) => {
      const { from, to } = rangeFor(filters, Date.now());
      return listAuditEvents({ from, to, vcluster, user: filters.user, verb: filters.verb, resource: filters.resource, namespace: filters.namespace, source: filters.source, outcome: filters.outcome, token: filters.token, limit: PAGE, before });
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [filterKey],
  );

  const loadFirst = useCallback(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);
    query()
      .then((res) => {
        if (cancelled) return;
        setItems(res.items);
        setNextBefore(res.nextBefore);
      })
      .catch((err) => !cancelled && setError(errorMessage(err)))
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
  }, [query]);

  useEffect(() => loadFirst(), [loadFirst]);
  usePolling(auto && !!config?.auditEnabled && items.length > 0 && cursorOf(items) === cursorOf(items.slice(0, PAGE)), 30_000, loadFirst);

  const loadMore = async () => {
    if (!nextBefore) return;
    setMore(true);
    try {
      const res = await query(nextBefore);
      setItems((prev) => mergePage(prev, res.items));
      setNextBefore(res.nextBefore);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setMore(false);
    }
  };

  // 选中事件：先用列表里的摘要，再拉详情
  const summary = items.find((e) => e.id === eventID) ?? null;
  const [detail, setDetail] = useState<AuditEventView | null>(null);
  const [detailErr, setDetailErr] = useState<string | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const loadDetail = useCallback(() => {
    if (!eventID) return;
    setDetailLoading(true);
    setDetailErr(null);
    getAuditEvent(eventID, isAdmin && !fixedVCluster ? filters.vcluster || undefined : vcluster)
      .then(setDetail)
      .catch((err) => setDetailErr(errorMessage(err)))
      .finally(() => setDetailLoading(false));
  }, [eventID, vcluster, fixedVCluster, filters.vcluster, isAdmin]);
  useEffect(() => {
    setDetail(null);
    loadDetail();
  }, [loadDetail]);

  if (config && !config.auditEnabled) {
    return <EmptyState title="审计未启用" hint="在平台配置里打开 audit.enabled 并配置 ClickHouse 后，这里会显示操作记录。" />;
  }

  const closeDrawer = () => selectEvent('');
  const selected = summary ?? detail;

  return (
    <>
      <AuditFilters value={filters} onChange={setFilters} clusters={clusters.data ?? []} showCluster={!fixedVCluster} />
      <div className="audit-toolbar">
        <span className="muted small">{loading && items.length === 0 ? '加载中…' : `已加载 ${items.length} 条${nextBefore ? '，还有更早的' : ''}`}</span>
        <div className="actions">
          <Button size="sm" variant={auto ? 'default' : 'ghost'} onClick={() => setAuto((a) => !a)} aria-pressed={auto}>
            自动刷新{auto ? '：开' : ''}
          </Button>
          <Button size="sm" onClick={loadFirst} disabled={loading}>
            刷新
          </Button>
        </div>
      </div>

      {loading && items.length === 0 ? (
        <Loading />
      ) : error && items.length === 0 ? (
        <ErrorState message={error} onRetry={loadFirst} />
      ) : items.length === 0 ? (
        <EmptyState title="该时间段内没有记录" hint="换一个时间段或放宽过滤条件。" />
      ) : (
        <Card flush>
          <Table hoverable compact>
            <thead>
              <tr>
                <th>时间</th>
                <th>来源</th>
                {!fixedVCluster && <th>集群</th>}
                <th>用户</th>
                <th>动作</th>
                <th>对象</th>
                <th>结果</th>
              </tr>
            </thead>
            <tbody>
              {items.map((e) => (
                <tr key={e.id} className={e.id === eventID ? 'is-selected' : undefined} onClick={() => selectEvent(e.id)}>
                  <td className="mono" title={formatEventTimeFull(e.ts)}>
                    {formatEventTime(e.ts, now)}
                  </td>
                  <td>
                    <Badge tone="neutral">{SOURCE_LABELS[e.source] ?? e.source}</Badge>
                  </td>
                  {!fixedVCluster && <td className="mono">{e.vcluster || <span className="muted">—</span>}</td>}
                  <td className="mono">{e.user}</td>
                  <td>{actionLabel(e)}</td>
                  <td className="mono">{objectLabel(e) || <span className="muted">—</span>}</td>
                  <td>
                    <StatusDot value={outcomeKind(e.responseCode) === 'ok' ? 'Running' : 'Failed'} label={<span className="mono">{e.responseCode}</span>} />
                  </td>
                </tr>
              ))}
            </tbody>
          </Table>
          <div className="audit-more">
            {nextBefore ? (
              <Button size="sm" onClick={loadMore} disabled={more}>
                {more ? '加载中…' : '加载更多'}
              </Button>
            ) : (
              <span className="muted small">没有更早的记录</span>
            )}
            {error && <span className="field-error small">{error}</span>}
          </div>
        </Card>
      )}

      {eventID && (
        <AuditDrawer
          summary={selected}
          detail={detail}
          loading={detailLoading}
          error={detailErr}
          onRetry={loadDetail}
          onClose={closeDrawer}
          onFilterToken={(id) => setFilters({ ...filters, token: id })}
        />
      )}
    </>
  );
}
