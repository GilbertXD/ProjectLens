import { useEffect, useRef } from 'react'
import { HighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { Compartment, EditorState, Text } from '@codemirror/state'
import { Decoration, EditorView, drawSelection, highlightActiveLineGutter, lineNumbers } from '@codemirror/view'
import { tags } from '@lezer/highlight'
import { languageForPath } from './languages'

const theme = EditorView.theme({
  '&': { color: 'var(--lens-ink)', backgroundColor: 'var(--lens-paper)' },
  '.cm-scroller': { fontFamily: 'var(--lens-font-code)', overflowX: 'auto', lineHeight: '1.65' },
  '.cm-content': { padding: '0.5rem 0' },
  '.cm-line': { paddingInline: '0.75rem' },
  '.cm-gutters': {
    color: 'var(--lens-graphite)', backgroundColor: 'var(--lens-rail)',
    borderRight: '1px solid var(--lens-rule)',
  },
  '.cm-lineNumbers .cm-gutterElement': { paddingInline: '0.5rem' },
  '&.cm-focused': { outlineOffset: '-2px' },
  '.cm-activeLineGutter': { backgroundColor: 'var(--lens-paper)', color: 'var(--lens-ink)', fontWeight: '700' },
  '.cm-line-focus': { backgroundColor: 'var(--lens-rail)', boxShadow: 'inset 2px 0 var(--lens-ink)' },
})

const highlights = HighlightStyle.define([
  { tag: [tags.keyword, tags.definitionKeyword, tags.bool, tags.null], color: 'var(--lens-ink)', fontWeight: '700' },
  { tag: [tags.comment, tags.meta], color: 'var(--lens-graphite)', fontStyle: 'italic' },
  { tag: [tags.string, tags.regexp, tags.number], color: 'var(--lens-graphite)' },
  { tag: [tags.typeName, tags.function(tags.definition(tags.variableName)), tags.heading], fontWeight: '600' },
])

export function CodeViewer({ path, content, highlightLine }: { path: string; content: string; highlightLine?: number }) {
  const host = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!host.current) return
    const doc = Text.of(content.split(/\r\n?|\n/))
    const language = new Compartment()
    const target = highlightLine !== undefined && Number.isInteger(highlightLine)
      && highlightLine > 0 && highlightLine <= doc.lines ? doc.line(highlightLine) : undefined
    const state = EditorState.create({
      doc,
      selection: { anchor: target?.from ?? 0 },
      extensions: [
        EditorState.readOnly.of(true), EditorView.editable.of(false),
        EditorView.contentAttributes.of({ 'aria-label': 'Source code', 'aria-readonly': 'true', tabindex: '0' }),
        lineNumbers(), drawSelection(), highlightActiveLineGutter(),
        theme, syntaxHighlighting(highlights), language.of([]),
        ...(target ? [EditorView.decorations.of(Decoration.set([
          Decoration.line({ attributes: { class: 'cm-line-focus', id: 'L' + target.number } }).range(target.from),
        ]))] : []),
      ],
    })
    const editor = new EditorView({ state, parent: host.current })
    // Layout APIs can be absent in DOM implementations without a rendering engine.
    if (target && typeof editor.dom.ownerDocument.createRange().getClientRects === 'function') {
      editor.dispatch({ effects: EditorView.scrollIntoView(target.from, { y: 'center' }) })
    }
    let active = true
    void languageForPath(path)?.then((support) => {
      if (active) editor.dispatch({ effects: language.reconfigure(support) })
    }, () => {
      // Keep the plain-text view available if a syntax pack fails to load.
    })
    return () => {
      active = false
      editor.destroy()
    }
  }, [path, content, highlightLine])

  return <div ref={host} className="code-viewer" />
}
