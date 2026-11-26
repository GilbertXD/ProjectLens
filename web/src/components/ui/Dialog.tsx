import { useId, useLayoutEffect, useRef } from 'react'
import type { ReactNode, RefObject } from 'react'

type DialogProps = {
  open: boolean
  title: string
  description?: string
  children: ReactNode
  actions?: ReactNode
  initialFocusRef?: RefObject<HTMLElement | null>
  onDismiss: () => void
}

export function Dialog({ open, title, description, children, actions, initialFocusRef, onDismiss }: DialogProps) {
  const ref = useRef<HTMLDialogElement>(null)
  const titleId = useId()
  const descriptionId = useId()

  useLayoutEffect(() => {
    const dialog = ref.current
    if (!dialog || !open) return

    dialog.showModal()
    initialFocusRef?.current?.focus()

    return () => {
      if (dialog.open) dialog.close()
    }
  }, [initialFocusRef, open])

  return (
    <dialog
      ref={ref}
      className="dialog"
      aria-labelledby={titleId}
      aria-describedby={description ? descriptionId : undefined}
      onCancel={(event) => {
        event.preventDefault()
        onDismiss()
      }}
    >
      <h2 id={titleId}>{title}</h2>
      {description && <p id={descriptionId}>{description}</p>}
      {children}
      {actions && <div className="dialog__actions">{actions}</div>}
    </dialog>
  )
}
