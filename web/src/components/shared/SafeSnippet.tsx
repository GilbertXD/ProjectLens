import { Fragment } from 'react'

function decode(text: string) {
  return text.replace(/&(?:#[0-9]+|#x[0-9a-f]+|[a-z][a-z0-9]+);/gi, (entity) =>
    new DOMParser().parseFromString('<span>' + entity + '</span>', 'text/html').body.textContent ?? entity)
}

export function SafeSnippet({ snippet }: { snippet: string }) {
  const parts = snippet.split(/(<mark>|<\/mark>)/)
  let marked = false
  const runs: { text: string; marked: boolean }[] = []
  for (const part of parts) {
    if (part === '<mark>' || part === '</mark>') {
      if (marked === (part === '<mark>')) return <>{decode(snippet)}</>
      marked = !marked
    } else if (part || marked) {
      runs.push({ text: decode(part), marked })
    }
  }
  if (marked) return <>{decode(snippet)}</>
  return <>{runs.map((run, index) => run.marked
    ? <mark key={index}>{run.text}</mark> : <Fragment key={index}>{run.text}</Fragment>)}</>
}
