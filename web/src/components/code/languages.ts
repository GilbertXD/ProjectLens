import type { LanguageSupport } from '@codemirror/language'

const js = () => import('@codemirror/lang-javascript').then(({ javascript }) => javascript())
const jsx = () => import('@codemirror/lang-javascript').then(({ javascript }) => javascript({ jsx: true }))
const markdown = () => import('@codemirror/lang-markdown').then(({ markdown }) => markdown())
const yaml = () => import('@codemirror/lang-yaml').then(({ yaml }) => yaml())

const languages = new Map<string, () => Promise<LanguageSupport>>([
  ['go', () => import('@codemirror/lang-go').then(({ go }) => go())],
  ['ts', () => import('@codemirror/lang-javascript').then(({ javascript }) => javascript({ typescript: true }))],
  ['tsx', () => import('@codemirror/lang-javascript').then(({ javascript }) => javascript({ typescript: true, jsx: true }))],
  ['js', js], ['jsx', jsx], ['mjs', js], ['cjs', js],
  ['json', () => import('@codemirror/lang-json').then(({ json }) => json())],
  ['md', markdown], ['markdown', markdown],
  ['html', () => import('@codemirror/lang-html').then(({ html }) => html())],
  ['css', () => import('@codemirror/lang-css').then(({ css }) => css())],
  ['py', () => import('@codemirror/lang-python').then(({ python }) => python())],
  ['yml', yaml], ['yaml', yaml],
  ['sql', () => import('@codemirror/lang-sql').then(({ sql }) => sql())],
])

export function languageForPath(path: string) {
  const extension = path.split('/').at(-1)?.match(/\.([^.]+)$/)?.[1].toLowerCase()
  return extension ? languages.get(extension)?.() : undefined
}
