import type { ReactNode } from 'react'

export function DataTable({ columns, rows, label, empty = 'No entries.' }: {
  columns: string[]
  rows: { key: string; cells: ReactNode[] }[]
  label: string
  empty?: string
}) {
  if (!rows.length) return <p className="rule-message">{empty}</p>
  return (
    <div className="table-scroll">
      <table className="data-table" aria-label={label}>
        <thead><tr>{columns.map((column) => <th key={column} scope="col">{column}</th>)}</tr></thead>
        <tbody>{rows.map(({ key, cells }) => (
          <tr key={key}>{cells.map((cell, index) => <td key={columns[index]}>{cell}</td>)}</tr>
        ))}</tbody>
      </table>
    </div>
  )
}
