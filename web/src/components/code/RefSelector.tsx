import { useState } from 'react'
import { ApiContent } from '@/api/ApiFeedback'
import { useRepositoryRefs } from '@/api/queries'
import { Pagination } from '@/components/ui/Pagination'
import { Select } from '@/components/ui/Select'
import type { GitRef } from '@/types/domain'

const limit = 50

export function RefSelector({ projectId, repoId, value = '', placeholder = 'Default ref', onSelect }: {
  projectId: string
  repoId: string
  value?: string
  placeholder?: string
  onSelect: (ref?: GitRef) => void
}) {
  const [offset, setOffset] = useState(0)
  const refs = useRepositoryRefs(projectId, repoId, { limit, offset })
  const page = refs.data
  const items = page?.items ?? []
  return <div className="ref-selector">
    <Select label="Git ref" value={value} onChange={(event) => onSelect(items.find((item) => item.ref === event.target.value))}>
      <option value="">{placeholder}</option>
      {value && !items.some((item) => item.ref === value) && <option value={value}>{value}</option>}
      {items.map((item) => <option key={item.ref} value={item.ref}>{item.name} ({item.type})</option>)}
    </Select>
    <ApiContent query={refs} loading="Loading Git refs…">
      {page && page.total > page.limit && <Pagination limit={page.limit} offset={page.offset} total={page.total} onOffsetChange={setOffset} />}
    </ApiContent>
  </div>
}
