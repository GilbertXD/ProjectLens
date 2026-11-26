import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'

afterEach(cleanup)

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
