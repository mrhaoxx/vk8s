import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import type { AuditEventView } from '../../api/types';
import { AuditDrawer } from './AuditDrawer';

const ev = {
  id: 'e1', ts: '2026-10-24T06:01:58.312Z', source: 'kube', vcluster: 'alice-dev', stage: 'ResponseComplete', level: 'Metadata',
  verb: 'get', user: 'alice', groups: [], impersonatedUser: '', sourceIPs: [], userAgent: 'kubectl', namespace: 'demo', apiGroup: '',
  apiVersion: 'v1', resource: 'pods', subresource: '', name: 'web', requestURI: '/api/v1/namespaces/demo/pods/web', responseCode: 200,
  responseReason: '', responseMessage: '', platformAction: '', platformTarget: '', requestTruncated: false,
  annotations: { 'vk8s.io/token-id': 'tok-3fa9c2e1b07d4a56' },
} as unknown as AuditEventView;

describe('AuditDrawer', () => {
  it('shows the access token behind a kube request and lets the user filter by it', () => {
    const onFilterToken = vi.fn();
    render(<AuditDrawer summary={ev} detail={null} loading={false} error={null} onRetry={() => {}} onClose={() => {}} onFilterToken={onFilterToken} />);
    const tok = screen.getByRole('button', { name: /tok-3fa9c2e1b07d4a56/ });
    fireEvent.click(tok);
    expect(onFilterToken).toHaveBeenCalledWith('tok-3fa9c2e1b07d4a56');
  });

  it('omits the token row for events without one', () => {
    render(<AuditDrawer summary={{ ...ev, annotations: {} } as AuditEventView} detail={null} loading={false} error={null} onRetry={() => {}} onClose={() => {}} onFilterToken={() => {}} />);
    expect(screen.queryByText('令牌')).toBeNull();
  });
});
