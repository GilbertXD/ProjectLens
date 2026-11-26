import type { ComponentProps } from 'react'

type ButtonProps = ComponentProps<'button'> & {
  variant?: 'primary' | 'secondary' | 'ghost' | 'danger'
  size?: 'sm' | 'md'
}

export function Button({
  variant = 'primary',
  size = 'md',
  type = 'button',
  className = '',
  ...props
}: ButtonProps) {
  return (
    <button
      {...props}
      type={type}
      className={`button button--${variant} button--${size} ${className}`.trim()}
    />
  )
}
