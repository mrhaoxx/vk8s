import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it } from 'vitest';
import { Breadcrumb } from './Breadcrumb';

describe('Breadcrumb', () => {
  it('links every item except the last', () => {
    render(
      <MemoryRouter>
        <Breadcrumb
          items={[
            { label: '我的集群', to: '/' },
            { label: 'alice-dev', to: '/vclusters/alice-dev' },
          ]}
        />
      </MemoryRouter>,
    );
    expect(screen.getByRole('link', { name: '我的集群' })).toHaveAttribute('href', '/');
    expect(screen.queryByRole('link', { name: 'alice-dev' })).toBeNull();
    expect(screen.getByText('alice-dev')).toBeInTheDocument();
  });
});
