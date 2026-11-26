import type { ComponentProps } from 'react'
import { Field, type FieldProps } from './Field'

type SelectProps = ComponentProps<'select'> & FieldProps

export function Select({ label, error, id, 'aria-describedby': describedBy, className = '', ...props }: SelectProps) {
  return (
    <Field label={label} error={error} id={id} describedBy={describedBy}>
      {(attributes) => <select {...props} {...attributes} className={`field-control ${className}`.trim()} />}
    </Field>
  )
}
