export interface KV {
  key: string;
  value: string;
}

export function recordToRows(rec: Record<string, string> | undefined | null): KV[] {
  return Object.entries(rec ?? {}).map(([key, value]) => ({ key, value }));
}

// 忽略空键；重复键后者覆盖前者。
export function rowsToRecord(rows: KV[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const r of rows) {
    const k = r.key.trim();
    if (k) out[k] = r.value.trim();
  }
  return out;
}

interface Props {
  rows: KV[];
  onChange: (rows: KV[]) => void;
  keyPlaceholder?: string;
  valuePlaceholder?: string;
  addText?: string;
  disabled?: boolean;
}

export function KeyValueEditor({
  rows,
  onChange,
  keyPlaceholder = '键',
  valuePlaceholder = '值',
  addText = '添加一行',
  disabled,
}: Props) {
  const update = (i: number, patch: Partial<KV>) => {
    onChange(rows.map((r, idx) => (idx === i ? { ...r, ...patch } : r)));
  };
  return (
    <div className="kv-editor">
      {rows.length === 0 && <p className="muted kv-empty">暂无条目</p>}
      {rows.map((r, i) => (
        <div className="kv-row" key={i}>
          <input
            type="text"
            value={r.key}
            placeholder={keyPlaceholder}
            onChange={(e) => update(i, { key: e.target.value })}
            disabled={disabled}
            spellCheck={false}
          />
          <span className="kv-eq">=</span>
          <input
            type="text"
            value={r.value}
            placeholder={valuePlaceholder}
            onChange={(e) => update(i, { value: e.target.value })}
            disabled={disabled}
            spellCheck={false}
          />
          <button
            type="button"
            className="icon-btn"
            aria-label="删除此行"
            onClick={() => onChange(rows.filter((_, idx) => idx !== i))}
            disabled={disabled}
          >
            ×
          </button>
        </div>
      ))}
      <button
        type="button"
        className="btn btn-sm"
        onClick={() => onChange([...rows, { key: '', value: '' }])}
        disabled={disabled}
      >
        {addText}
      </button>
    </div>
  );
}
