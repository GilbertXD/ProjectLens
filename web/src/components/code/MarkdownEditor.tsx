import { useEffectEvent, useLayoutEffect, useRef } from 'react'
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands'
import { markdown } from '@codemirror/lang-markdown'
import { HighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { EditorState, Text } from '@codemirror/state'
import { EditorView, keymap } from '@codemirror/view'
import { tags } from '@lezer/highlight'

const highlights = HighlightStyle.define([
  { tag: [tags.heading, tags.strong], fontWeight: '600' },
  { tag: tags.emphasis, fontStyle: 'italic' },
  { tag: [tags.link, tags.url], textDecoration: 'underline' },
  { tag: [tags.processingInstruction, tags.quote], color: 'var(--lens-graphite)' },
])

type MarkdownEditorProps = {
  value: string
  onChange: (value: string) => void
  'aria-label'?: string
  'aria-labelledby'?: string
}

export function MarkdownEditor({
  value, onChange, 'aria-label': label, 'aria-labelledby': labelledBy,
}: MarkdownEditorProps) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView>(null)
  const reportChange = useEffectEvent(onChange)
  const makeState = useEffectEvent((doc = Text.of(value.split(/\r\n?|\n/))) => EditorState.create({
    doc,
    extensions: [
      markdown(), history(), keymap.of([...defaultKeymap, ...historyKeymap]),
      EditorView.lineWrapping, syntaxHighlighting(highlights),
      EditorView.contentAttributes.of({
        'aria-multiline': 'true',
        ...(labelledBy ? { 'aria-labelledby': labelledBy } : { 'aria-label': label ?? 'Markdown editor' }),
      }),
      EditorView.updateListener.of((update) => {
        if (update.docChanged) reportChange(update.state.doc.toString())
      }),
    ],
  }))
  useLayoutEffect(() => {
    if (!host.current) return
    const editor = new EditorView({ state: makeState(), parent: host.current })
    view.current = editor
    return () => {
      editor.destroy()
      view.current = null
    }
  }, [])

  useLayoutEffect(() => {
    const editor = view.current
    if (!editor) return
    const doc = Text.of(value.split(/\r\n?|\n/))
    if (!editor.state.doc.eq(doc)) editor.setState(makeState(doc))
  }, [value])

  useLayoutEffect(() => {
    const content = view.current?.contentDOM
    if (!content) return
    if (labelledBy) {
      content.setAttribute('aria-labelledby', labelledBy)
      content.removeAttribute('aria-label')
    } else {
      content.setAttribute('aria-label', label ?? 'Markdown editor')
      content.removeAttribute('aria-labelledby')
    }
  }, [label, labelledBy])

  return <div ref={host} className="markdown-editor" />
}
