import { useOutletContext } from 'react-router-dom'
import type { Project } from '@/types/domain'

export function useProject() {
  const project = useOutletContext<Project | undefined>()
  if (!project) throw new Error('Project workspace context is required')
  return project
}
