import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { DescriptionList } from './DescriptionList';

describe('DescriptionList', () => {
  it('renders a dash for empty values', () => {
    render(
      <DescriptionList
        items={[
          { term: 'A', value: '' },
          { term: 'B', value: null },
          { term: 'C', value: 'x' },
        ]}
      />,
    );
    expect(screen.getAllByText('—')).toHaveLength(2);
    expect(screen.getByText('x')).toBeInTheDocument();
  });
});
