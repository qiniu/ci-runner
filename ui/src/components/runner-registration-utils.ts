import type { AppTFunction } from "@/i18n"

export function runnerRegistrationTargetLabel(t: AppTFunction, group = "", organization?: string) {
  const name = group.trim()
  if (!name) return t("user.repositoryRegistration")
  return organization
    ? t("user.organizationRegistration", { organization, group: name })
    : t("user.ownerOrganizationRegistration", { group: name })
}
