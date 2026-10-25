import type { AuditFilterState, RangePreset } from './filters';
import { VERB_OPTIONS } from './labels';
import { Button } from '../../components/Button';

interface Props {
  value: AuditFilterState;
  onChange: (next: AuditFilterState) => void;
  // 集群下拉的候选；fixedVCluster 时不渲染集群项
  clusters?: string[];
  showCluster: boolean;
}

const PRESETS: { key: RangePreset; label: string }[] = [
  { key: '1h', label: '1 小时' },
  { key: '24h', label: '24 小时' },
  { key: '7d', label: '7 天' },
  { key: 'custom', label: '自定义' },
];

function Seg<T extends string>({ options, value, onChange, label }: { options: { key: T; label: string }[]; value: T; onChange: (v: T) => void; label: string }) {
  return (
    <div className="seg" role="group" aria-label={label}>
      {options.map((o) => (
        <button key={o.key} type="button" className={o.key === value ? 'seg-item is-on' : 'seg-item'} aria-pressed={o.key === value} onClick={() => onChange(o.key)}>
          {o.label}
        </button>
      ))}
    </div>
  );
}

// 审计页过滤条：全部改动即时回调，由父组件写 URL。
export function AuditFilters({ value, onChange, clusters = [], showCluster }: Props) {
  const set = <K extends keyof AuditFilterState>(k: K, v: AuditFilterState[K]) => onChange({ ...value, [k]: v });
  const isDefault = JSON.stringify(value) === JSON.stringify({ ...value, preset: '24h', from: undefined, to: undefined, vcluster: '', user: '', verb: '', resource: '', namespace: '', source: '', outcome: '', token: '' });
  return (
    <div className="audit-filters">
      <Seg label="时间段" options={PRESETS} value={value.preset} onChange={(p) => set('preset', p)} />
      {value.preset === 'custom' && (
        <>
          <input type="datetime-local" aria-label="起" value={value.from ?? ''} onChange={(e) => set('from', e.target.value)} />
          <input type="datetime-local" aria-label="止" value={value.to ?? ''} onChange={(e) => set('to', e.target.value)} />
        </>
      )}
      {showCluster && (
        <select aria-label="集群" value={value.vcluster} onChange={(e) => set('vcluster', e.target.value)}>
          <option value="">全部集群</option>
          {clusters.map((c) => (
            <option key={c} value={c}>
              {c}
            </option>
          ))}
        </select>
      )}
      <input type="search" aria-label="用户" placeholder="用户" value={value.user} onChange={(e) => set('user', e.target.value.trim())} className="audit-input" />
      {value.token && (
        <button type="button" className="seg-item is-on mono" aria-label={`清除令牌过滤 ${value.token}`} title="点击清除令牌过滤" onClick={() => set('token', '')}>
          令牌 {value.token} ×
        </button>
      )}
      <select aria-label="动词" value={value.verb} onChange={(e) => set('verb', e.target.value)}>
        <option value="">全部动词</option>
        {VERB_OPTIONS.map((v) => (
          <option key={v} value={v}>
            {v}
          </option>
        ))}
      </select>
      <input type="search" aria-label="资源" placeholder="资源，如 pods" value={value.resource} onChange={(e) => set('resource', e.target.value.trim())} className="audit-input" />
      <input type="search" aria-label="命名空间" placeholder="命名空间" value={value.namespace} onChange={(e) => set('namespace', e.target.value.trim())} className="audit-input" />
      <Seg
        label="来源"
        options={[
          { key: '', label: '全部' },
          { key: 'kube', label: '集群内' },
          { key: 'platform', label: '平台' },
        ]}
        value={value.source}
        onChange={(s) => set('source', s)}
      />
      <Seg
        label="结果"
        options={[
          { key: '', label: '全部' },
          { key: 'success', label: '成功' },
          { key: 'failure', label: '失败' },
        ]}
        value={value.outcome}
        onChange={(o) => set('outcome', o)}
      />
      <Button size="sm" variant="ghost" onClick={() => onChange({ preset: '24h', vcluster: showCluster ? '' : value.vcluster, user: '', verb: '', resource: '', namespace: '', source: '', outcome: '', token: '' })} disabled={isDefault}>
        重置
      </Button>
    </div>
  );
}
