import type { AdminGitHubInstallation } from "@/admin-types"

export function filterGitHubInstallations(
  installations: AdminGitHubInstallation[],
  query: string,
): AdminGitHubInstallation[] {
  const normalizedQuery = query.trim().toLowerCase()
  if (!normalizedQuery) return installations
  return installations.filter((installation) => [
    installation.id,
    installation.account_id,
    installation.account_type,
    installation.account_login,
    installation.account_name,
  ].some((value) => String(value ?? "").toLowerCase().includes(normalizedQuery)))
}

export function githubAccountInitial(installation: AdminGitHubInstallation): string {
  return (installation.account_login || installation.account_name || "?").trim().charAt(0).toUpperCase() || "?"
}

export function githubRepositoryURL(repository: string): string {
  return `https://github.com/${repository.split("/").map(encodeURIComponent).join("/")}`
}
