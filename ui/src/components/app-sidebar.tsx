import { Activity, ClipboardList, CloudCog, Github, ListTree, ScrollText, Settings2, Stethoscope, UsersRound } from "lucide-react"
import { useTranslation } from "react-i18next"

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

type AppSidebarProps = {
  section: string
  onSectionChange: (section: string) => void
}

export function AppSidebar({
  section,
  onSectionChange,
}: AppSidebarProps) {
  const { t } = useTranslation()
  const items = [
    { id: "overview", label: t("sidebar.overview"), icon: Activity },
    { id: "accounts", label: t("sidebar.accounts"), icon: UsersRound },
    { id: "github_accounts", label: t("sidebar.githubAppAccounts"), icon: Github },
    { id: "runner_requests", label: t("sidebar.runnerRequests"), icon: ListTree },
    { id: "runner_specs", label: t("sidebar.runnerSpecs"), icon: Settings2 },
    { id: "sandbox_service", label: t("sidebar.sandboxService"), icon: CloudCog },
    { id: "match", label: t("sidebar.matchTest"), icon: ClipboardList },
    { id: "audit", label: t("sidebar.audit"), icon: ScrollText },
    { id: "diagnostics", label: t("sidebar.diagnostics"), icon: Stethoscope },
  ]

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
                return (
                  <SidebarMenuItem key={item.id}>
                    <SidebarMenuButton
                      isActive={section === item.id}
                      className="data-[active=true]:bg-primary/10 data-[active=true]:text-primary data-[active=true]:shadow-sm"
                      onClick={() => onSectionChange(item.id)}
                    >
                      <Icon className="text-sidebar-primary" />
                      <span className="font-medium">{item.label}</span>
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
