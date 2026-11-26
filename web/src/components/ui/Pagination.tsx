import { Button } from './Button'

type PaginationProps = {
  offset: number
  limit: number
  onOffsetChange: (offset: number) => void
} & (
  | { total: number; hasMore?: never; itemCount?: never }
  | { total?: never; hasMore: boolean; itemCount: number }
)

export function Pagination({ offset, limit, total, hasMore, itemCount, onOffsetChange }: PaginationProps) {
  const count = total === undefined ? itemCount : Math.max(0, Math.min(limit, total - offset))
  const canAdvance = total === undefined ? hasMore : offset + limit < total
  const range = count > 0 ? `${offset + 1}–${offset + count}` : 'No items on this page'

  return (
    <nav className="pagination" aria-label="Pagination">
      <span aria-live="polite">{count > 0 && total !== undefined ? `${range} of ${total}` : range}</span>
      <div className="pagination__actions">
        <Button
          variant="secondary"
          size="sm"
          disabled={offset === 0}
          onClick={() => onOffsetChange(Math.max(0, offset - limit))}
        >
          Previous
        </Button>
        <Button variant="secondary" size="sm" disabled={!canAdvance} onClick={() => onOffsetChange(offset + limit)}>
          Next
        </Button>
      </div>
    </nav>
  )
}
