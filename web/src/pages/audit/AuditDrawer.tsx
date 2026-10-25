import { useEffect } from 'react';
import type { AuditEventView } from '../../api/types';
import { Button } from '../../components/Button';
import { CopyButton } from '../../components/CopyButton';
import { DescriptionList } from '../../components/DescriptionList';
import { StatusDot } from '../../components/StatusDot';
import { ErrorState, Loading } from '../../components/State';
import { formatEventTimeFull } from './filters';
import { SOURCE_LABELS, actionLabel, objectLabel, outcomeKind, reasonText } from './labels';

interface Props {
  // 列表里的那一条（先显示；深链接打开时可能还没有），detail 为拉到的完整事件（含请求体）
  summary: AuditEventView | null;
  detail: AuditEventView | null;
  loading: boolean;
  error: string | null;
  onRetry: () => void;
  onClose: () => void;
  // 点击令牌 ID → 按该令牌过滤列表
  onFilterToken?: (id: string) => void;
}

function prettyJSON(s: string): string {
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch {
    return s;
  }
}

// 右侧抽屉：Esc 或点遮罩关闭；窄屏全屏。
export function AuditDrawer({ summary, detail, loading, error, onRetry, onClose, onFilterToken }: Props) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [onClose]);

  const e = detail ?? summary;
  const decision = e?.annotations['authorization.k8s.io/decision'];
  const tokenID = e?.annotations['vk8s.io/token-id'] ?? '';
  const reason = e?.annotations['authorization.k8s.io/reason'];
  const body = detail?.requestObject ?? '';

  return (
    <>
      <div className="drawer-backdrop" onClick={onClose} />
      <aside className="drawer" role="dialog" aria-label="事件详情" aria-modal="true">
        <div className="drawer-head">
          <h2>事件详情</h2>
          <Button size="sm" variant="ghost" onClick={onClose} aria-label="关闭">
            ×
          </Button>
        </div>
        <div className="drawer-body">
          {!e ? (
            loading ? (
              <Loading text="加载事件…" />
            ) : (
              <ErrorState message={error ?? '事件不存在或不在你可见的范围内'} onRetry={onRetry} />
            )
          ) : (
            <>
              <DescriptionList
                compact
                items={[
                  { term: '时间', value: formatEventTimeFull(e.ts) },
                  { term: '来源', value: e.vcluster ? `${SOURCE_LABELS[e.source] ?? e.source} · ${e.vcluster}` : SOURCE_LABELS[e.source] ?? e.source },
                  {
                    term: '用户',
                    value: (
                      <>
                        <span className="mono">{e.user || '—'}</span>
                        {e.groups.length > 0 && <span className="muted small"> ({e.groups.join(', ')})</span>}
                        {e.impersonatedUser && <span className="muted small"> 扮演 {e.impersonatedUser}</span>}
                      </>
                    ),
                  },
                  ...(tokenID
                    ? [
                        {
                          term: '令牌',
                          value: (
                            <button type="button" className="btn btn-xs mono" title="只看这个令牌的操作" onClick={() => onFilterToken?.(tokenID)}>
                              {tokenID}
                            </button>
                          ),
                        },
                      ]
                    : []),
                  { term: '来源 IP', value: e.sourceIPs.length ? <span className="mono">{e.sourceIPs.join(', ')}</span> : '' },
                  { term: '客户端', value: <span className="muted wrap">{e.userAgent}</span> },
                  { term: '动作', value: <b>{actionLabel(e)}</b> },
                  { term: '对象', value: <span className="mono">{objectLabel(e)}</span> },
                  { term: '请求', value: <span className="mono muted wrap">{e.requestURI}</span> },
                  {
                    term: '结果',
                    value: (
                      <StatusDot
                        value={outcomeKind(e.responseCode) === 'ok' ? 'Running' : 'Failed'}
                        label={`${e.responseCode}${reasonText(e) ? ` ${reasonText(e)}` : ''}${e.responseMessage ? ` · ${e.responseMessage}` : ''}`}
                      />
                    ),
                  },
                  { term: '授权', value: decision ? <span className="muted wrap">{decision}{reason ? ` · ${reason}` : ''}</span> : '' },
                  { term: '审计 ID', value: <span className="mono muted">{e.id}</span> },
                ]}
              />
              <div className="drawer-section">
                <div className="drawer-section-head">
                  <span className="strong">请求体（Request）</span>
                  {body && <CopyButton text={body} label="复制" className="btn btn-xs" />}
                </div>
                {loading && !detail ? (
                  <Loading text="加载请求体…" />
                ) : error ? (
                  <ErrorState message={error} onRetry={onRetry} />
                ) : body ? (
                  <>
                    {e.requestTruncated && <p className="field-note warn">请求体超过 64 KiB，已截断。</p>}
                    <pre className="json-block">{prettyJSON(body)}</pre>
                  </>
                ) : (
                  <p className="muted">该事件没有记录请求体{e.level === 'Metadata' ? '（元数据级别）' : ''}。</p>
                )}
              </div>
            </>
          )}
        </div>
      </aside>
    </>
  );
}
