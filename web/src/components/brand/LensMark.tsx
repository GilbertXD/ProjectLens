import type { SVGProps } from 'react'

type LensMarkProps = Omit<SVGProps<SVGSVGElement>, 'children'> & {
  label?: string
}

export function LensMark({ label, ...props }: LensMarkProps) {
  return (
    <svg
      width={64}
      height={64}
      {...props}
      viewBox="0 0 64 64"
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      role={label ? 'img' : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
      focusable="false"
    >
      <circle cx="32" cy="32" r="24" />
      <path d="M32 2v12m0 36v12M2 32h12m36 0h12M32 25v14m-7-7h14" />
      <circle cx="32" cy="32" r="13" />
    </svg>
  )
}
