import { type ReactNode, useCallback, useEffect, useRef, useState } from "react"
import { RefreshCw } from "lucide-react"
import { useTranslation } from "react-i18next"

import type { PublicRunnerTemplate, SandboxInstance, SandboxTemplate } from "@/admin-types"
import appI18n from "@/i18n"
import {
  formatOptionalTime,
  loadSandboxInstances,
  loadSandboxTemplates,
  useSandboxRegions,
  sandboxInstancesViewState,
  type SandboxCatalogRequest,
} from "@/components/sandbox-catalog-utils"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"

async function loadPublicRunnerTemplates(request: SandboxCatalogRequest) {
  const data = await request("/api/public/runner-templates")
  return Array.isArray(data) ? (data as PublicRunnerTemplate[]) : []
}

function Header({
  title,
  description,
  region,
  loading,
  onRegion,
  onRefresh,
  children,
}: {
  title: string
  description: string
  region: string
  loading: boolean
  onRegion: (value: string) => void
  onRefresh: () => void
  children?: ReactNode
}) {
  const sandboxRegions = useSandboxRegions()
  const { t } = useTranslation()
  return (
    <CardHeader className="flex flex-col gap-3 pb-3 2xl:flex-row 2xl:items-center 2xl:justify-between">
      <div>
        <CardTitle className="text-base">{title}</CardTitle>
        <CardDescription className="mt-1">{description}</CardDescription>
      </div>
      <div className="flex w-full flex-wrap justify-end gap-2 2xl:w-auto">
        <Select value={region} onValueChange={onRegion}>
          <SelectTrigger className="min-w-[200px] max-w-[280px]">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {sandboxRegions.map((item) => (
              <SelectItem key={item.id} value={item.id}>
                {item.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {children}
        <Button variant="outline" size="icon" onClick={onRefresh} disabled={loading} aria-label={t("common.refresh")}>
          <RefreshCw className={loading ? "animate-spin" : ""} />
        </Button>
      </div>
    </CardHeader>
  )
}

export function SandboxTemplatesSection({
  request,
  installationID,
}: {
  request: SandboxCatalogRequest
  installationID?: number
}) {
  const sandboxRegions = useSandboxRegions()
  const [region, setRegion] = useState("")
  const [publicItems, setPublicItems] = useState<PublicRunnerTemplate[]>([])
  const [scopedItems, setScopedItems] = useState<SandboxTemplate[]>([])
  const [publicLoading, setPublicLoading] = useState(false)
  const [scopedLoading, setScopedLoading] = useState(false)
  const [publicError, setPublicError] = useState("")
  const [scopedError, setScopedError] = useState("")
  const publicLoadGeneration = useRef(0)
  const scopedLoadGeneration = useRef(0)

  const loadPublic = useCallback(async () => {
    const generation = ++publicLoadGeneration.current
    setPublicLoading(true)
    setPublicError("")
    try {
      const data = await loadPublicRunnerTemplates(request)
      if (generation === publicLoadGeneration.current) {
        setPublicItems(data)
      }
    } catch (cause) {
      if (generation === publicLoadGeneration.current) {
        setPublicError(cause instanceof Error ? cause.message : appI18n.t("user.loadSandboxTemplatesFailed"))
      }
    } finally {
      if (generation === publicLoadGeneration.current) {
        setPublicLoading(false)
      }
    }
  }, [request])

  const loadScoped = useCallback(async () => {
    const generation = ++scopedLoadGeneration.current
    setScopedLoading(true)
    setScopedError("")
    setScopedItems([])
    try {
      const data = await loadSandboxTemplates(request, region, installationID)
      if (generation === scopedLoadGeneration.current) {
        setScopedItems(data)
      }
    } catch (cause) {
      if (generation === scopedLoadGeneration.current) {
        setScopedError(cause instanceof Error ? cause.message : appI18n.t("user.loadSandboxTemplatesFailed"))
      }
    } finally {
      if (generation === scopedLoadGeneration.current) {
        setScopedLoading(false)
      }
    }
  }, [installationID, region, request])

  useEffect(() => {
    if (region === "" && sandboxRegions.length > 0) {
      setRegion(sandboxRegions[0].id)
    }
  }, [region, sandboxRegions])

  useEffect(() => {
    void loadPublic()
    return () => {
      publicLoadGeneration.current += 1
    }
  }, [loadPublic])

  useEffect(() => {
    if (!region) return
    void loadScoped()
    return () => {
      scopedLoadGeneration.current += 1
    }
  }, [loadScoped, region])

  return (
    <SandboxTemplateCatalog
      publicTemplates={publicItems}
      publicLoading={publicLoading}
      publicError={publicError}
      scopedTemplates={scopedItems}
      scopedLoading={scopedLoading}
      scopedError={scopedError}
      region={region}
      onRegion={setRegion}
      onPublicRefresh={() => void loadPublic()}
      onScopedRefresh={() => void loadScoped()}
    />
  )
}

export function SandboxTemplateCatalog({
  publicTemplates,
  publicLoading,
  publicError,
  scopedTemplates,
  scopedLoading,
  scopedError,
  region = "",
  onRegion = () => {},
  onPublicRefresh = () => {},
  onScopedRefresh = () => {},
}: {
  publicTemplates: PublicRunnerTemplate[]
  publicLoading: boolean
  publicError: string
  scopedTemplates: SandboxTemplate[]
  scopedLoading: boolean
  scopedError: string
  region?: string
  onRegion?: (value: string) => void
  onPublicRefresh?: () => void
  onScopedRefresh?: () => void
}) {
  const { t } = useTranslation()

  return (
    <div className="space-y-4">
      <Card className="rounded-lg">
        <CardHeader className="flex flex-row items-center justify-between gap-3 pb-3">
          <div>
            <CardTitle className="text-base">{t("user.publicRunnerTemplates")}</CardTitle>
            <CardDescription className="mt-1">{t("user.publicRunnerTemplatesDescription")}</CardDescription>
          </div>
          <Button
            variant="outline"
            size="icon"
            onClick={onPublicRefresh}
            disabled={publicLoading}
            aria-label={t("common.refresh")}
          >
            <RefreshCw className={publicLoading ? "animate-spin" : ""} />
          </Button>
        </CardHeader>
        <CardContent>
          {publicError ? (
            <p className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">{publicError}</p>
          ) : (
            <div className="overflow-x-auto rounded-md border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("common.template")}</TableHead>
                    <TableHead>{t("user.runnerSpecs")}</TableHead>
                    <TableHead>{t("user.workflowLabels")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {publicTemplates.map((item) => (
                    <TableRow key={item.default_template_name}>
                      <TableCell className="font-medium">{item.default_template_name}</TableCell>
                      <TableCell>
                        {item.runner_spec_names.map((name) => <div key={name}>{name}</div>)}
                      </TableCell>
                      <TableCell>
                        {item.workflow_labels.map((labels) => <div key={labels.join("\u0000")}>{labels.join(", ")}</div>)}
                      </TableCell>
                    </TableRow>
                  ))}
                  {!publicLoading && publicTemplates.length === 0 ? (
                    <TableRow>
                      <TableCell colSpan={3} className="h-32 text-center text-muted-foreground">
                        {t("user.noPublicRunnerTemplates")}
                      </TableCell>
                    </TableRow>
                  ) : null}
                </TableBody>
              </Table>
            </div>
          )}
        </CardContent>
      </Card>

      <Card className="rounded-lg">
        <Header
          title={t("user.providerTemplates")}
          description={t("user.providerTemplatesDescription")}
          region={region}
          loading={scopedLoading}
          onRegion={onRegion}
          onRefresh={onScopedRefresh}
        />
        <CardContent>
          {scopedError ? (
            <p className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">{scopedError}</p>
          ) : (
            <div className="overflow-x-auto rounded-md border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("common.template")}</TableHead>
                    <TableHead>{t("common.status")}</TableHead>
                    <TableHead>{t("common.resources")}</TableHead>
                    <TableHead>{t("common.visibility")}</TableHead>
                    <TableHead className="text-right">{t("user.spawns")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {scopedTemplates.map((item) => (
                    <TableRow key={item.template_id}>
                      <TableCell>
                        <div className="font-medium">{item.aliases?.[0] || item.template_id}</div>
                        <div className="max-w-[360px] truncate text-xs text-muted-foreground">{item.template_id}</div>
                      </TableCell>
                      <TableCell>{item.build_status || t("common.unknown")}</TableCell>
                      <TableCell>
                        {t("user.catalogTemplateResources", {
                          cpu: item.cpu_count,
                          memory: item.memory_mb,
                          disk: item.disk_size_mb,
                        })}
                      </TableCell>
                      <TableCell>{item.public ? t("common.public") : t("common.private")}</TableCell>
                      <TableCell className="text-right tabular-nums">{item.spawn_count}</TableCell>
                    </TableRow>
                  ))}
                  {!scopedLoading && scopedTemplates.length === 0 ? (
                    <TableRow>
                      <TableCell colSpan={5} className="h-32 text-center text-muted-foreground">
                        {t("user.noTemplatesInRegion")}
                      </TableCell>
                    </TableRow>
                  ) : null}
                </TableBody>
              </Table>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

export function SandboxesSection({
  request,
  installationID,
}: {
  request: SandboxCatalogRequest
  installationID?: number
}) {
  const sandboxRegions = useSandboxRegions()
  const { t, i18n } = useTranslation()
  const [region, setRegion] = useState("")
  const [template, setTemplate] = useState("all")
  const [templates, setTemplates] = useState<SandboxTemplate[]>([])
  const [items, setItems] = useState<SandboxInstance[]>([])
  const [templatesLoading, setTemplatesLoading] = useState(false)
  const [instancesLoading, setInstancesLoading] = useState(false)
  const [templatesError, setTemplatesError] = useState("")
  const [instancesError, setInstancesError] = useState("")
  const templateLoadGeneration = useRef(0)
  const instanceLoadGeneration = useRef(0)

  const loadTemplates = useCallback(async () => {
    const generation = ++templateLoadGeneration.current
    setTemplatesLoading(true)
    setTemplatesError("")
    setTemplates([])
    try {
      const data = await loadSandboxTemplates(request, region, installationID)
      if (generation === templateLoadGeneration.current) {
        setTemplates(data)
      }
    } catch (cause) {
      if (generation === templateLoadGeneration.current) {
        setTemplatesError(cause instanceof Error ? cause.message : appI18n.t("user.loadSandboxTemplatesFailed"))
      }
    } finally {
      if (generation === templateLoadGeneration.current) {
        setTemplatesLoading(false)
      }
    }
  }, [installationID, region, request])

  const loadInstances = useCallback(async () => {
    const generation = ++instanceLoadGeneration.current
    setInstancesLoading(true)
    setInstancesError("")
    setItems([])
    try {
      const templateID = template === "all" ? "" : template
      const data = await loadSandboxInstances(request, region, installationID, templateID)
      if (generation === instanceLoadGeneration.current) {
        setItems(data)
      }
    } catch (cause) {
      if (generation === instanceLoadGeneration.current) {
        setInstancesError(cause instanceof Error ? cause.message : appI18n.t("user.loadSandboxesFailed"))
      }
    } finally {
      if (generation === instanceLoadGeneration.current) {
        setInstancesLoading(false)
      }
    }
  }, [installationID, region, request, template])

  useEffect(() => {
    if (region === "" && sandboxRegions.length > 0) {
      setRegion(sandboxRegions[0].id)
    }
  }, [region, sandboxRegions])

  useEffect(() => {
    if (!region) return
    void loadTemplates()
    return () => {
      templateLoadGeneration.current += 1
    }
  }, [loadTemplates, region])

  useEffect(() => {
    if (!region) return
    void loadInstances()
    return () => {
      instanceLoadGeneration.current += 1
    }
  }, [loadInstances, region])

  const { loading, error, filterDisabled } = sandboxInstancesViewState({
    templatesLoading,
    instancesLoading,
    templatesError,
    instancesError,
  })

  return (
    <Card className="rounded-lg">
      <Header
        title={t("user.sandboxInstances")}
        description={t("user.catalogInstancesDescription")}
        region={region}
        loading={loading}
        onRegion={(value) => {
          setRegion(value)
          setTemplate("all")
        }}
        onRefresh={() => {
          void loadTemplates()
          void loadInstances()
        }}
      >
        <Select value={template} onValueChange={setTemplate} disabled={filterDisabled}>
          <SelectTrigger className="min-w-[200px] max-w-[280px]">
            <SelectValue placeholder={t("user.filterTemplate")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t("user.allTemplates")}</SelectItem>
            {templates.map((item) => (
              <SelectItem key={item.template_id} value={item.template_id}>
                {item.aliases?.[0] || item.template_id}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Header>
      <CardContent>
        {error ? (
          <p className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</p>
        ) : (
          <div className="overflow-x-auto rounded-md border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("common.sandbox")}</TableHead>
                  <TableHead>{t("user.state")}</TableHead>
                  <TableHead>{t("common.template")}</TableHead>
                  <TableHead>{t("common.resources")}</TableHead>
                  <TableHead>{t("common.started")}</TableHead>
                  <TableHead>{t("common.expires")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((item) => (
                  <TableRow key={item.sandbox_id}>
                    <TableCell>
                      <div className="font-medium">{item.alias || item.sandbox_id}</div>
                      <div className="max-w-[300px] truncate text-xs text-muted-foreground">{item.sandbox_id}</div>
                    </TableCell>
                    <TableCell>{item.state}</TableCell>
                    <TableCell>
                      <div className="max-w-[260px] truncate">{item.template_id}</div>
                    </TableCell>
                    <TableCell>
                      {t("user.catalogInstanceResources", {
                        cpu: item.cpu_count,
                        memory: item.memory_mb,
                      })}
                    </TableCell>
                    <TableCell>{formatOptionalTime(item.started_at, i18n.resolvedLanguage)}</TableCell>
                    <TableCell>{formatOptionalTime(item.expires_at, i18n.resolvedLanguage)}</TableCell>
                  </TableRow>
                ))}
                {!loading && items.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={6} className="h-32 text-center text-muted-foreground">
                      {t("user.noSandboxesForFilters")}
                    </TableCell>
                  </TableRow>
                ) : null}
              </TableBody>
            </Table>
          </div>
        )}
      </CardContent>
    </Card>
  )
}
