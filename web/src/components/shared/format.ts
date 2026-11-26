export function formatDate(value?: string) {
  if (!value) return '—'
  return new Intl.DateTimeFormat('en', { dateStyle: 'medium', timeStyle: 'short', timeZone: 'UTC' }).format(new Date(value))
}

export const shortSha = (hash: string) => hash.slice(0, 8)
export const projectPath = (projectId: string) => '/projects/' + encodeURIComponent(projectId)
export const repositoryPath = (projectId: string, repoId: string) => projectPath(projectId) + '/repositories/' + encodeURIComponent(repoId)
export const documentPath = (projectId: string, docId: string) => projectPath(projectId) + '/documents/' + encodeURIComponent(docId)
export const policyNames = { files: 'Files', commits: 'Commits', github_activity: 'GitHub activity' }
