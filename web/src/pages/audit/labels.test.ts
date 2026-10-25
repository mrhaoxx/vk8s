import { describe, expect, it } from 'vitest';
import type { AuditEventView } from '../../api/types';
import { actionLabel, objectLabel, outcomeKind } from './labels';

describe('labels', () => {
  it('labels kube and platform events', () => {
    expect(actionLabel({ source: 'kube', verb: 'create', resource: 'pods', subresource: 'exec', apiGroup: '' } as AuditEventView)).toBe('create pods/exec');
    expect(actionLabel({ source: 'kube', verb: 'delete', resource: 'deployments', apiGroup: 'apps' } as AuditEventView)).toBe('delete deployments.apps');
    expect(actionLabel({ source: 'platform', platformAction: 'member.add' } as AuditEventView)).toBe('添加成员');
    expect(actionLabel({ source: 'platform', platformAction: 'something.new' } as AuditEventView)).toBe('something.new');
    expect(objectLabel({ source: 'kube', namespace: 'demo', name: 'web' } as AuditEventView)).toBe('demo/web');
    expect(objectLabel({ source: 'kube', namespace: 'demo', name: '' } as AuditEventView)).toBe('demo/');
    expect(objectLabel({ source: 'platform', platformTarget: 'dave' } as AuditEventView)).toBe('dave');
    expect(outcomeKind(403)).toBe('err');
    expect(outcomeKind(201)).toBe('ok');
  });
});
