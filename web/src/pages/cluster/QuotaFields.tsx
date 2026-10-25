import { Field } from '../../components/Field';
import { KeyValueEditor } from '../../components/KeyValueEditor';
import type { QuotaDraft } from './quotaDraft';

interface Props {
  draft: QuotaDraft;
  onChange: (d: QuotaDraft) => void;
  disabled?: boolean;
}

export function QuotaFields({ draft, onChange, disabled }: Props) {
  const set = (patch: Partial<QuotaDraft>) => onChange({ ...draft, ...patch });
  return (
    <div className="grid-2">
      <Field label="CPU" hint="如 16、500m" required>
        <input type="text" value={draft.cpu} onChange={(e) => set({ cpu: e.target.value })} disabled={disabled} />
      </Field>
      <Field label="内存" hint="如 64Gi" required>
        <input type="text" value={draft.memory} onChange={(e) => set({ memory: e.target.value })} disabled={disabled} />
      </Field>
      <Field label="Pod 数" required>
        <input type="text" inputMode="numeric" value={draft.pods} onChange={(e) => set({ pods: e.target.value })} disabled={disabled} />
      </Field>
      <Field label="Service 数" required>
        <input type="text" inputMode="numeric" value={draft.services} onChange={(e) => set({ services: e.target.value })} disabled={disabled} />
      </Field>
      <Field label="存储" hint="PVC 总量，如 200Gi" required>
        <input type="text" value={draft.storage} onChange={(e) => set({ storage: e.target.value })} disabled={disabled} />
      </Field>
      <div className="span-2">
        <span className="field-label">扩展资源</span>
        <KeyValueEditor
          rows={draft.extended}
          onChange={(extended) => set({ extended })}
          keyPlaceholder="nvidia.com/gpu"
          valuePlaceholder="8"
          addText="添加扩展资源"
          disabled={disabled}
        />
      </div>
    </div>
  );
}
