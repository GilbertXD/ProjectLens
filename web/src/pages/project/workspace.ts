import { useOutletContext } from 'react-router-dom'
import type { Project } from '@/types/domain'

export function useProject() {
  return useOutletContext<Project>()
}
