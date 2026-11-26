import type { ComponentProps } from 'react'
import { Field, type FieldProps } from './Field'

type InputProps = ComponentProps<'input'> & FieldProps

export function Input({ label, error, id, 'aria-describedby': describedBy, className = '', ...props }: InputProps) {
  return (
    <Field label={label} error={error} id={id} describedBy={describedBy}>
      {(attributes) => <input {...props} {...attributes} className={`field-control ${className}`.trim()} />}
    </Field>
  )
}
