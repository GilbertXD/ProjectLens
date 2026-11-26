import { useId } from 'react'
import type { ReactNode } from 'react'

export type FieldProps = {
  label: string
  error?: string
}

type ControlAttributes = {
  id: string
  'aria-describedby'?: string
  'aria-invalid'?: true
}

type FieldContainerProps = FieldProps & {
  id?: string
  describedBy?: string
  children: (attributes: ControlAttributes) => ReactNode
}

export function Field({ id, label, error, describedBy, children }: FieldContainerProps) {
  const generatedId = useId()
  const controlId = id ?? generatedId
  const errorId = `${controlId}-error`
  const attributes: ControlAttributes = { id: controlId, 'aria-describedby': describedBy }

  if (error) {
    attributes['aria-invalid'] = true
    attributes['aria-describedby'] = describedBy ? `${describedBy} ${errorId}` : errorId
  }

  return (
    <div className="field">
      <label htmlFor={controlId}>{label}</label>
      {children(attributes)}
      {error && <p id={errorId} className="field-error">{error}</p>}
    </div>
  )
}
