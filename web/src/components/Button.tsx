import type { ButtonHTMLAttributes } from 'react';
import { Link, type LinkProps } from 'react-router-dom';

export type ButtonVariant = 'default' | 'primary' | 'danger' | 'danger-ghost' | 'ghost';
export type ButtonSize = 'md' | 'sm' | 'xs';

interface StyleProps {
  variant?: ButtonVariant;
  size?: ButtonSize;
  block?: boolean;
  className?: string;
}

// 统一按钮类名：default 与 md 不加变体类。
export function buttonClass({ variant = 'default', size = 'md', block, className }: StyleProps): string {
  const parts = ['btn'];
  if (variant !== 'default') parts.push(`btn-${variant}`);
  if (size !== 'md') parts.push(`btn-${size}`);
  if (block) parts.push('btn-block');
  if (className) parts.push(className);
  return parts.join(' ');
}

// 普通按钮；type 缺省为 button，表单提交按钮显式传 type="submit"。
export function Button({ variant, size, block, className, type = 'button', ...rest }: StyleProps & ButtonHTMLAttributes<HTMLButtonElement>) {
  return <button type={type} className={buttonClass({ variant, size, block, className })} {...rest} />;
}

// 站内链接样式的按钮。
export function ButtonLink({ variant, size, block, className, ...rest }: StyleProps & LinkProps) {
  return <Link className={buttonClass({ variant, size, block, className })} {...rest} />;
}
