import { describe, expect, it } from 'vitest';
import { clusterColor, hashHue } from './colors';

describe('clusterColor', () => {
  it('is stable per name and differs between names', () => {
    expect(clusterColor('alice-dev', false)).toBe(clusterColor('alice-dev', false));
    expect(hashHue('alice-dev')).not.toBe(hashHue('team-x'));
    expect(clusterColor('x', false)).toMatch(/^hsl\(\d+ 32% 46%\)$/);
    expect(clusterColor('x', true)).toMatch(/^hsl\(\d+ 28% 40%\)$/);
  });
});
