import type { ReactNode } from 'react';

interface Props {
  label: string;
  hint?: ReactNode;
  error?: string | null;
  required?: boolean;
  children: ReactNode;
}

// 表单字段包装：label + 控件 + 提示/错误。
export function Field({ label, hint, error, required, children }: Props) {
  return (
    <label className={`field${error ? ' field-invalid' : ''}`}>
      <span className="field-label">
        {label}
        {required && <span className="required">*</span>}
      </span>
      {children}
      {error ? <small className="field-error">{error}</small> : hint ? <small className="muted">{hint}</small> : null}
    </label>
  );
}
