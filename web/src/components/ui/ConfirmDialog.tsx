import { useRef } from 'react'
import type { ReactNode } from 'react'
import { Button } from './Button'
import { Dialog } from './Dialog'

type ConfirmDialogProps = {
  open: boolean
  title: string
  description?: string
  impact?: ReactNode
  confirmLabel: string
  pending?: boolean
  onConfirm: () => void
  onCancel: () => void
}

export function ConfirmDialog({
  open, title, description, impact, confirmLabel, pending = false, onConfirm, onCancel,
}: ConfirmDialogProps) {
  const cancelRef = useRef<HTMLButtonElement>(null)

  return (
    <Dialog
      open={open}
      title={title}
      description={description}
      initialFocusRef={cancelRef}
      onDismiss={onCancel}
      actions={(
        <>
          <Button ref={cancelRef} variant="secondary" onClick={onCancel}>Cancel</Button>
          <Button variant="danger" disabled={pending} onClick={onConfirm}>{confirmLabel}</Button>
        </>
      )}
    >
      {impact && <div className="dialog__impact">{impact}</div>}
    </Dialog>
  )
}
