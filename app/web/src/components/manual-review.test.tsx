import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { ResultCounts } from '../pages/DashboardPage'
import { StatusBadge } from './ui'

describe('manual review presentation', () => {
  it('renders a distinct conclusion badge', () => {
    const markup = renderToStaticMarkup(<StatusBadge status="manual_review" />)
    expect(markup).toContain('status-manual_review')
    expect(markup).toContain('人工复核')
  })

  it('renders manual review as an independent aggregate count', () => {
    const markup = renderToStaticMarkup(<ResultCounts summary={{
      safe: 1, unsafe: 2, manual_review: 3, error: 4, not_applicable: 5,
    }} />)
    expect(markup).toContain('title="人工复核"')
    expect(markup).toContain('>3<')
  })
})
