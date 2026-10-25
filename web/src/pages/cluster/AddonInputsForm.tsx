import type { AddonInputView } from '../../api/types';

interface Props {
  inputs: AddonInputView[];
  value: Record<string, string>;
  onChange: (next: Record<string, string>) => void;
}

// 应用参数表单：string / bool / enum / password 四种控件；未填的项不写进结果（后端取默认值）。
export function AddonInputsForm({ inputs, value, onChange }: Props) {
  if (inputs.length === 0) return null;
  const set = (name: string, v: string) => {
    const next = { ...value };
    if (v === '') delete next[name];
    else next[name] = v;
    onChange(next);
  };
  return (
    <div className="addon-inputs">
      {inputs.map((i) => {
        const id = `addon-input-${i.name}`;
        const cur = value[i.name] ?? '';
        if (i.type === 'bool') {
          const checked = (value[i.name] ?? i.default ?? 'false') === 'true';
          return (
            <label key={i.name} className="addon-input check" htmlFor={id}>
              <input id={id} type="checkbox" checked={checked} onChange={(e) => set(i.name, e.target.checked ? 'true' : 'false')} />
              {i.label}
            </label>
          );
        }
        return (
          <label key={i.name} className="addon-input" htmlFor={id}>
            <span>
              {i.label}
              {i.required && <span className="field-required"> *</span>}
            </span>
            {i.type === 'enum' ? (
              <select id={id} value={cur || i.default || ''} onChange={(e) => set(i.name, e.target.value)}>
                {(i.options ?? []).map((o) => (
                  <option key={o} value={o}>
                    {o}
                  </option>
                ))}
              </select>
            ) : (
              <input
                id={id}
                type={i.type === 'password' ? 'password' : 'text'}
                autoComplete={i.type === 'password' ? 'new-password' : 'off'}
                placeholder={i.default ?? ''}
                value={cur}
                onChange={(e) => set(i.name, e.target.value)}
              />
            )}
          </label>
        );
      })}
    </div>
  );
}
