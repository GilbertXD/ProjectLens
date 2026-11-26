import Markdown, { defaultUrlTransform } from 'react-markdown'
import type { Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'

const embeddedHeadings: Components = {
  h1: ({ children }) => <h3 className="markdown-title">{children}</h3>,
  h2: ({ children }) => <h4 className="markdown-subtitle">{children}</h4>,
  h3: ({ children }) => <h5>{children}</h5>,
  h4: ({ children }) => <h6>{children}</h6>,
  h5: ({ children }) => <h6>{children}</h6>,
  h6: ({ children }) => <h6>{children}</h6>,
}

export function SafeMarkdown({ content }: { content: string }) {
  return (
    <div className="markdown">
      <Markdown
        skipHtml
        components={embeddedHeadings}
        disallowedElements={['img']}
        remarkPlugins={[remarkGfm]}
        urlTransform={(url) => {
          const safe = defaultUrlTransform(url)
          if (!safe || safe.startsWith('//') || /[\\\u0000-\u001f\u007f]/.test(safe)) return undefined
          const protocol = /^([a-z][a-z0-9+.-]*):/i.exec(safe)?.[1].toLowerCase()
          return !protocol || ['http', 'https', 'mailto'].includes(protocol) ? safe : undefined
        }}
      >
        {content}
      </Markdown>
    </div>
  )
}
