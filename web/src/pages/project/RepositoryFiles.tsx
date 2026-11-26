import { useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { DataTable } from '@/components/shared/DataTable'
import { SectionRule } from '@/components/shared/SectionRule'
import { formatDate, repositoryPath, shortSha } from '@/components/shared/format'
import { Button } from '@/components/ui/Button'
import type { RepositoryPreview } from '@/mocks/fixtures'
import type { SourceFile } from '@/types/domain'

interface Folder {
  folders: Map<string, Folder>
  files: SourceFile[]
}

function tree(files: SourceFile[]): Folder {
  const root: Folder = { folders: new Map(), files: [] }
  for (const file of files) {
    let folder = root
    for (const segment of file.path.split('/').slice(0, -1)) {
      let child = folder.folders.get(segment)
      if (!child) {
        child = { folders: new Map(), files: [] }
        folder.folders.set(segment, child)
      }
      folder = child
    }
    folder.files.push(file)
  }
  return root
}

function FileTree({ folder, selected }: { folder: Folder; selected: string }) {
  return <ul className="file-tree">
    {[...folder.folders].map(([name, child]) => <li key={name}>
      <details open><summary>{name}/</summary><FileTree folder={child} selected={selected} /></details>
    </li>)}
    {folder.files.map(({ path }) => <li key={path}>
      <Link to={'?path=' + encodeURIComponent(path)} aria-current={path === selected ? 'page' : undefined}>{path.split('/').at(-1)}</Link>
    </li>)}
  </ul>
}

export function RepositoryFiles({ preview }: { preview: RepositoryPreview }) {
  const [params] = useSearchParams()
  const active = preview.snapshots.find(({ status }) => status === 'active')
  const files = preview.files.filter(({ snapshot_id }) => snapshot_id === active?.id)
  const path = params.get('path') ?? files[0]?.path
  const file = files.find((item) => item.path === path)
  const line = Number(params.get('line'))
  const [overlay, setOverlay] = useState<'history' | 'blame'>()
  const repo = preview.repository
  const repoUrl = repositoryPath(repo.project_id, repo.id)

  useEffect(() => {
    if (file && Number.isInteger(line) && line > 0) {
      document.getElementById('L' + line)?.scrollIntoView?.({ block: 'nearest' })
    }
  }, [file, line])

  if (!files.length) return <SectionRule title="Files">
    <p className="rule-message">No indexed files. <Link className="text-link" to={repoUrl + '/snapshots'}>Publish a snapshot</Link> to scan this repository.</p>
  </SectionRule>

  const lines = file?.content?.split('\n') ?? []
  const blame = preview.blame[file?.path ?? ''] ?? []
  const history = preview.fileHistory[file?.path ?? ''] ?? []

  return <div className="file-browser">
    <aside aria-label="File tree" className="tree-pane">
      <h2>File tree</h2>
      <code className="muted">{active?.resolved_ref ?? preview.refs[0]?.name ?? repo.default_branch}</code>
      <FileTree folder={tree(files)} selected={path} />
    </aside>
    <section className="code-pane" aria-label="Read-only code viewer">
      {file ? <>
        <div className="section-heading">
          <div className="stacked-cell"><h2><code>{file.path}</code></h2>
            <span className="muted">{file.line_count} lines, {file.size_bytes} bytes</span></div>
          <div className="inline-actions">
            <Button size="sm" variant="secondary" aria-pressed={overlay === 'history'}
              onClick={() => setOverlay(overlay === 'history' ? undefined : 'history')}>File history</Button>
            <Button size="sm" variant="secondary" aria-pressed={overlay === 'blame'}
              onClick={() => setOverlay(overlay === 'blame' ? undefined : 'blame')}>Blame</Button>
          </div>
        </div>
        {overlay === 'history' && <DataTable label="File history" columns={['Commit', 'Change', 'Author', 'Date']}
          empty="No imported history for this file."
          rows={history.map((entry) => ({ key: entry.hash, cells: [
            <Link className="text-link" to={repoUrl + '/commits/' + entry.hash}>{entry.subject}</Link>,
            <code>{entry.change_type}</code>, entry.author_name, formatDate(entry.authored_at),
          ] }))} />}
        {overlay === 'blame' && !blame.length && <p className="rule-message">No blame information for this file.</p>}
        {lines.length ? <div className="code-scroll" tabIndex={0} aria-label="Source code">
          <ol className="code-lines">
            {lines.map((text, index) => {
              const number = index + 1
              const owner = blame[index]
              return <li key={number} id={'L' + number} className={number === line ? 'selected-line' : undefined}>
                <a href={'#L' + number} className="line-number" aria-label={'Line ' + number}>{number}</a>
                {overlay === 'blame' && owner && <Link className="blame-label" title={owner.summary}
                  to={repoUrl + '/commits/' + owner.commit_hash}>{shortSha(owner.commit_hash)} {owner.author_name}</Link>}
                <code>{text || '\u00a0'}</code>
              </li>
            })}
          </ol>
        </div> : <p className="rule-message">No text content is available for this file.</p>}
      </> : <p className="rule-message">This file is absent from the active snapshot. Choose a file in the tree.</p>}
    </section>
  </div>
}
