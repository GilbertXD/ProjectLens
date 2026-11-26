import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { SafeMarkdown } from '../SafeMarkdown'
import { SafeSnippet } from '../SafeSnippet'

describe('SafeSnippet', () => {
  it('renders only exact mark delimiters and decodes entities as text', () => {
    const { container } = render(<SafeSnippet snippet={'&#32;<mark>Git &amp; &#x47;o</mark> &lt;mark&gt;plain&lt;/mark&gt;'} />)
    expect(container.querySelectorAll('mark')).toHaveLength(1)
    expect(container.querySelector('mark')?.textContent).toBe('Git & Go')
    expect(container.textContent).toBe(' Git & Go <mark>plain</mark>')
  })

  it('neutralizes literal and escaped HTML around and inside a mark', () => {
    const { container } = render(<SafeSnippet snippet={'<img src=x onerror=alert(1)><mark>&lt;script&gt;x&lt;/script&gt;</mark><script>x</script>'} />)
    expect(container.querySelector('img, script')).toBeNull()
    expect(container.querySelector('mark')?.textContent).toBe('<script>x</script>')
    expect(container.textContent).toContain('<img src=x onerror=alert(1)>')
  })

  it.each(['<mark>open', 'close</mark>', '<mark>a<mark>b</mark></mark>', '<mark class="raw">a</mark>'])(
    'renders malformed delimiters as text (%s)', (snippet) => {
      const { container } = render(<SafeSnippet snippet={snippet} />)
      expect(container.querySelector('mark')).toBeNull()
      expect(container.textContent).toBe(snippet)
    },
  )
})

describe('SafeMarkdown', () => {
  it('disables raw HTML and unsafe URLs while preserving GFM and safe links', () => {
    const { container } = render(<SafeMarkdown content={`# Safe preview

<img src=x onerror=alert(1)>
<script>alert(1)</script>

[Unsafe](java&#x73;cript:alert%281%29)
[Safe](https://example.test/guide)

| Scope | Status |
| --- | --- |
| Project | Ready |`} />)
    expect(screen.getByRole('heading', { name: 'Safe preview' })).toBeVisible()
    expect(container.querySelector('img, script, a[href^="javascript:"]')).toBeNull()
    expect(screen.getByText('Unsafe')).not.toHaveAttribute('href')
    expect(screen.getByRole('link', { name: 'Safe' })).toHaveAttribute('href', 'https://example.test/guide')
    expect(screen.getByRole('table')).toBeVisible()
  })
})
