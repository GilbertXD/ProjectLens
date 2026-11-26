import type { ComponentProps } from 'react'
import { Field, type FieldProps } from './Field'

type TextareaProps = ComponentProps<'textarea'> & FieldProps

export function Textarea({ label, error, id, 'aria-describedby': describedBy, className = '', ...props }: TextareaProps) {
  return (
    <Field label={label} error={error} id={id} describedBy={describedBy}>
      {(attributes) => <textarea {...props} {...attributes} className={`field-control ${className}`.trim()} />}
    </Field>
  )
}
