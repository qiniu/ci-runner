import { BarChart3, Activity, ClipboardList, CloudCog, Github, ListTree, ScrollText, Settings2, Stethoscope, UsersRound } from "lucide-react"
import { useTranslation } from "react-i18next"

import { adminSectionPath, type AdminSection } from "@/admin-types"
import { AppLink } from "@/components/app-link"
import { QiniuRunnerLogo } from "@/components/qiniu-runner-logo"
import {
  Sidebar,
  SidebarContent,
  SidebarGroup,
  SidebarGroupContent,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
} from "@/components/ui/sidebar"

export function AppSidebar({ section }: { section: AdminSection }) {
  const { t } = useTranslation()
  const items = [
    { id: "overview", label: t("sidebar.overview"), icon: Activity },
    { id: "reports", label: t("sidebar.reports"), icon: BarChart3 },
    { id: "accounts", label: t("sidebar.accounts"), icon: UsersRound },
    { id: "github_accounts", label: t("sidebar.githubAppAccounts"), icon: Github },
    { id: "runner_requests", label: t("sidebar.runnerRequests"), icon: ListTree },
    { id: "runner_specs", label: t("sidebar.runnerSpecs"), icon: Settings2 },
    { id: "sandbox_service", label: t("sidebar.sandboxService"), icon: CloudCog },
    { id: "match", label: t("sidebar.matchTest"), icon: ClipboardList },
    { id: "audit", label: t("sidebar.audit"), icon: ScrollText },
    { id: "diagnostics", label: t("sidebar.diagnostics"), icon: Stethoscope },
  ] as const

  return (
    <Sidebar collapsible="offcanvas">
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton asChild size="lg" className="data-[slot=sidebar-menu-button]:!p-1.5">
              <a href="/" aria-label={t("common.productHome")}>
                <QiniuRunnerLogo />
              </a>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>

      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupContent>
            <SidebarMenu>
              {items.map((item) => {
                const Icon = item.icon
                const isActive = section === item.id
                return (
                  <SidebarMenuItem key={item.id}>
                    <SidebarMenuButton
                      asChild
                      isActive={isActive}
                      className="data-[active=true]:bg-primary/10 data-[active=true]:text-primary data-[active=true]:shadow-sm"
                    >
                      <AppLink href={adminSectionPath(item.id)} aria-current={isActive ? "location" : undefined}>
                        <Icon className="text-sidebar-primary" />
                        <span className="font-medium">{item.label}</span>
                      </AppLink>
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                )
              })}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>
    </Sidebar>
  )
}
