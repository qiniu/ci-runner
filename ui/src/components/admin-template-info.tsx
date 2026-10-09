import { RefreshCw } from "lucide-react"
import { useTranslation } from "react-i18next"

import { formatTime } from "@/admin-format"
import type { TemplateStatus } from "@/hooks/use-admin-template-status"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

const templateLookupErrors = {
  sandbox_service_not_configured: "admin.templateServiceNotConfigured",
  sandbox_service_config_error: "admin.templateServiceConfigError",
  sandbox_template_access_denied: "admin.templateAccessDenied",
  template_not_found: "admin.templateNotFound",
  public_template_invalid: "admin.templateNameUnresolved",
  template_validation_timeout: "admin.templateLookupTimeout",
  template_state_unavailable: "admin.templateStateUnavailable",
} as const

export function AdminTemplateInfo({
  status,
  empty = false,
  onRetry,
  helpID,
  showSuccessMessage = true,
  reference,
  variant = "panel",
}: {
  status: TemplateStatus | null
  empty?: boolean
  onRetry: () => void
  helpID?: string
  showSuccessMessage?: boolean
  reference?: string
  variant?: "panel" | "plain"
}) {
  const { t, i18n } = useTranslation()
  const statusKey = empty
    ? "admin.templateReferenceDescription"
    : !status
      ? "admin.templateChecking"
      : status.error
        ? templateLookupErrors[
            status.error as keyof typeof templateLookupErrors
          ] || "admin.templateVisibilityUnknown"
        : !status.public
          ? "admin.templatePrivate"
          : !status.runnable
            ? "admin.templateNotRunnable"
            : "admin.templatePublic"
  const configure =
    status?.error === "sandbox_service_not_configured" ||
    status?.error === "sandbox_service_config_error" ||
    status?.error === "sandbox_template_access_denied"
  const template = status?.template
  const pairedReference = Boolean(reference && template && !status?.error)
  return (
    <div className="@container grid min-w-0 gap-2">
      {showSuccessMessage || statusKey !== "admin.templatePublic" ? (
        <div
          id={helpID}
          className="text-xs leading-5 text-muted-foreground"
          role="status"
        >
          {t(statusKey)}{" "}
          {configure ? (
            <a
              href="/admin/sandbox_service"
              className="underline underline-offset-2"
            >
              {t("admin.configureTemplateValidation")}
            </a>
          ) : null}
          {status?.error ? (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="ml-1 h-auto px-2 py-1 text-xs"
              onClick={onRetry}
            >
              <RefreshCw className="size-3" />
              {t("admin.retryTemplateLookup")}
            </Button>
          ) : null}
        </div>
      ) : null}
      {!status?.error &&
      status?.runnable &&
      ["failed", "error"].includes(template?.build_status || "") ? (
        <p className="text-xs leading-5 text-muted-foreground" role="status">
          {t("admin.templateLatestBuildFailed")}
        </p>
      ) : null}
      {reference || (template && !status?.error) ? (
        <dl
          aria-label={t("admin.sandboxTemplateDetails")}
          className={cn(
            "grid min-w-0 gap-x-6 gap-y-2 text-xs @min-[36rem]:grid-cols-2 @min-[60rem]:grid-cols-6",
            variant === "panel" && "rounded-md border bg-muted/30 p-3",
          )}
        >
          {[
            ...(reference ? [[t("admin.templateReference"), reference]] : []),
            ...(template && !status?.error
              ? [
                  [t("admin.templateName"), template.names?.join(", ") || "—"],
                  [t("admin.templateID"), template.template_id || "—"],
                  [
                    t("admin.templateConfiguration"),
                    t("admin.templateConfigurationValue", {
                      cpu: template.cpu_count || "—",
                      memory: template.memory_mb || "—",
                      disk: template.disk_size_mb || "—",
                    }),
                  ],
                  [t("admin.envdVersion"), template.envd_version || "—"],
                  [
                    t("admin.templateVisibility"),
                    status?.public ? t("common.yes") : t("common.no"),
                  ],
                  [
                    t("admin.templateCreatedAt"),
                    template.created_at
                      ? formatTime(template.created_at, i18n.resolvedLanguage)
                      : "—",
                  ],
                  [
                    t("admin.templateUpdatedAt"),
                    template.updated_at
                      ? formatTime(template.updated_at, i18n.resolvedLanguage)
                      : "—",
                  ],
                ]
              : []),
          ].map(([label, value], index) => (
            <div
              key={label}
              className={cn(
                "grid min-w-0 grid-cols-[auto_minmax(0,1fr)] items-baseline gap-x-2 leading-5",
                index === 0 && !pairedReference
                  ? "col-span-full"
                  : pairedReference && index === 1
                    ? "@min-[60rem]:col-span-4"
                    : "@min-[60rem]:col-span-2",
              )}
            >
              <dt className="text-muted-foreground">{label}</dt>
              <dd className="min-w-0 whitespace-normal [overflow-wrap:anywhere]">
                {value}
              </dd>
            </div>
          ))}
        </dl>
      ) : null}
    </div>
  )
}
