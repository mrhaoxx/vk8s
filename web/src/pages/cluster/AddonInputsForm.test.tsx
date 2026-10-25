import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import type { AddonInputView } from '../../api/types';
import { AddonInputsForm } from './AddonInputsForm';

const inputs: AddonInputView[] = [
  { name: 'banner', label: '横幅', type: 'string' },
  { name: 'webui', label: '启用 Web 界面', type: 'bool', default: 'false' },
  { name: 'tier', label: '规格', type: 'enum', options: ['small', 'large'], default: 'small' },
  { name: 'adminPassword', label: '管理员密码', type: 'password' },
];

describe('AddonInputsForm', () => {
  it('renders string / bool / enum / password controls and reports values', () => {
    const onChange = vi.fn();
    render(<AddonInputsForm inputs={inputs} value={{}} onChange={onChange} />);
    fireEvent.change(screen.getByLabelText('横幅'), { target: { value: 'hi' } });
    expect(onChange).toHaveBeenLastCalledWith({ banner: 'hi' });
    fireEvent.click(screen.getByLabelText('启用 Web 界面'));
    expect(onChange).toHaveBeenLastCalledWith({ webui: 'true' });
    fireEvent.change(screen.getByLabelText('规格'), { target: { value: 'large' } });
    expect(onChange).toHaveBeenLastCalledWith({ tier: 'large' });
    const pw = screen.getByLabelText('管理员密码') as HTMLInputElement;
    expect(pw.type).toBe('password');
  });
});
