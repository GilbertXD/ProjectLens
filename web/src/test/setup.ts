import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'

afterEach(cleanup)

// CodeMirror measures text ranges. jsdom has no layout engine.
if (typeof Range.prototype.getClientRects !== 'function') {
  Object.defineProperty(Range.prototype, 'getClientRects', {
    configurable: true,
    writable: true,
    value(): DOMRectList { return Object.assign([], { item: () => null }) },
  })
}

if (typeof Range.prototype.getBoundingClientRect !== 'function') {
  Object.defineProperty(Range.prototype, 'getBoundingClientRect', {
    configurable: true,
    writable: true,
    value(): DOMRect { return new DOMRect() },
  })
}

// jsdom exposes the dialog element without implementing the native modal methods.
if (!HTMLDialogElement.prototype.showModal) {
  Object.defineProperties(HTMLDialogElement.prototype, {
    showModal: {
      configurable: true,
      value(this: HTMLDialogElement) { this.setAttribute('open', '') },
    },
    close: {
      configurable: true,
      value(this: HTMLDialogElement) { this.removeAttribute('open') },
    },
  })
}
