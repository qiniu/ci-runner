import { githubContextFromPath } from "@/github-context-routes"
import { ArrowLeft, Github, LoaderCircle, SearchX, ShieldX } from "lucide-react"
import type { ReactNode } from "react"
import { useTranslation } from "react-i18next"

import { signInURL } from "@/app-load-policy"
import { QiniuRunnerLogo } from "@/components/qiniu-runner-logo"

export function SignInPage({
  oauthEnabled,
  returnTo,
}: {
  oauthEnabled: boolean
  returnTo: string
}) {
  const { t } = useTranslation()
  const destination = t(protectedDestinationLabel(returnTo))

  return (
    <RouteMessagePage
      eyebrow={destination}
      title={t("auth.signIn")}
      description={t("auth.signInDescription", { destination: destination.toLowerCase() })}
    >
      {oauthEnabled ? (
        <div className="flex flex-col items-start gap-4">
          <a
            href={signInURLFromReturnTo(returnTo)}
            className="inline-flex h-11 items-center justify-center gap-2 rounded-md bg-[#00aae7] px-5 text-sm font-semibold text-[#041018] transition-colors hover:bg-[#27c5f5] focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[#00aae7]"
          >
            <Github className="h-4 w-4" />
            {t("auth.continueGitHub")}
          </a>
          <a
            href="/docs/getting-started/hosted"
            className="text-sm font-semibold text-[#006b91] hover:text-[#004e6b]"
          >
            {t("auth.readHostedGuide")}
          </a>
        </div>
      ) : (
        <div className="rounded-lg border border-[#f0d5a8] bg-[#fff9ed] px-4 py-3 text-sm leading-6 text-[#6f4b11]" role="status">
          <strong className="block">{t("auth.signInUnavailable")}</strong>
          {t("auth.oauthNotConfigured")}
        </div>
      )}
    </RouteMessagePage>
  )
}

export function AccessDeniedPage({
  login,
  onSignOut,
}: {
  login?: string
  onSignOut: () => void
}) {
  const { t } = useTranslation()
  return (
    <RouteMessagePage
      eyebrow={t("auth.adminConsole")}
      icon={<ShieldX className="h-5 w-5" />}
      title={t("auth.accessDenied")}
      description={t("auth.accessDeniedDescription", { account: login || t("auth.thisGitHubAccount") })}
    >
      <div className="flex flex-col gap-3 sm:flex-row">
        <a
          href="/jobs"
          className="inline-flex h-11 items-center justify-center rounded-md bg-[#00aae7] px-5 text-sm font-semibold text-[#041018] transition-colors hover:bg-[#27c5f5]"
        >
          {t("auth.openJobs")}
        </a>
        <button
          type="button"
          onClick={onSignOut}
          className="inline-flex h-11 items-center justify-center rounded-md border border-[#cbdde6] bg-white px-5 text-sm font-semibold text-[#213a48] transition-colors hover:bg-[#f2fcff]"
        >
          {t("common.signOut")}
        </button>
      </div>
    </RouteMessagePage>
  )
}

export function NotFoundPage() {
  const { t } = useTranslation()
  return (
    <RouteMessagePage
      eyebrow="404"
      icon={<SearchX className="h-5 w-5" />}
      title={t("auth.pageNotFound")}
      description={t("auth.pageNotFoundDescription")}
    />
  )
}

export function SessionLoadingPage() {
  const { t } = useTranslation()
  return (
    <RouteMessagePage
      eyebrow={t("auth.secureRoute")}
      icon={<LoaderCircle className="h-5 w-5 animate-spin" />}
      title={t("auth.checkingSession")}
      description={t("auth.checkingSessionDescription")}
    />
  )
}

export function SessionErrorPage({ onRetry }: { onRetry: () => void }) {
  const { t } = useTranslation()
  return (
    <RouteMessagePage
      eyebrow={t("auth.secureRoute")}
      title={t("auth.sessionError")}
      description={t("auth.sessionErrorDescription")}
    >
      <button
        type="button"
        onClick={onRetry}
        className="inline-flex h-11 items-center justify-center rounded-md bg-[#00aae7] px-5 text-sm font-semibold text-[#041018] transition-colors hover:bg-[#27c5f5] focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[#00aae7]"
      >
        {t("auth.tryAgain")}
      </button>
    </RouteMessagePage>
  )
}

function RouteMessagePage({
  eyebrow,
  icon,
  title,
  description,
  children,
}: {
  eyebrow: string
  icon?: ReactNode
  title: string
  description: string
  children?: ReactNode
}) {
  const { t } = useTranslation()
  return (
    <main className="brand-home relative flex min-h-screen items-center overflow-hidden bg-[#f7fcff] px-5 py-16 text-[#0a0d12]">
      <div className="brand-grid absolute inset-0 opacity-70" />
      <div className="absolute -right-40 -top-48 h-[560px] w-[560px] rounded-full bg-[#00aae7]/15 blur-[110px]" />
      <section className="relative mx-auto w-full max-w-lg rounded-2xl border border-[#d5e4eb] bg-white p-7 shadow-[0_30px_90px_rgba(7,49,73,0.12)] sm:p-10">
        <a href="/" aria-label={t("auth.homeAria")} className="inline-flex">
          <QiniuRunnerLogo />
        </a>
        {icon ? (
          <div className="mt-12 flex h-11 w-11 items-center justify-center rounded-lg border border-[#bdeaf8] bg-[#f2fcff] text-[#006b91]">
            {icon}
          </div>
        ) : null}
        <p className={`${icon ? "mt-7" : "mt-12"} font-mono text-[11px] font-semibold uppercase tracking-[0.16em] text-[#006b91]`}>
          {eyebrow}
        </p>
        <h1 className="mt-3 text-3xl font-semibold tracking-[-0.045em] sm:text-4xl">{title}</h1>
        <p className="mt-4 text-[15px] leading-7 text-[#596b75]">{description}</p>
        {children ? <div className="mt-8">{children}</div> : null}
        <a href="/" className="mt-9 inline-flex items-center gap-2 text-sm font-semibold text-[#006b91] hover:text-[#004e6b]">
          <ArrowLeft className="h-4 w-4" />
          {t("auth.backHome")}
        </a>
      </section>
    </main>
  )
}

function signInURLFromReturnTo(returnTo: string): string {
  const parsed = returnTo.match(/^([^?]*)(.*)$/)
  return signInURL(parsed?.[1] || "/jobs", parsed?.[2] || "")
}

type ProtectedDestinationKey =
  | "user.deployments"
  | "auth.pullRequestJobs"
  | "auth.workflowRunJobs"
  | "auth.branchJobs"
  | "auth.repositories"
  | "auth.accountSettings"
  | "auth.adminConsole"
  | "auth.jobDetails"
  | "auth.jobs"

function protectedDestinationLabel(returnTo: string): ProtectedDestinationKey {
  const [path, query = ""] = returnTo.split("?")
  const context = githubContextFromPath(path, query)
  if (path === "/deployments" || path.startsWith("/deployments/") || (context && /\/deployments(?:\/[^/]+)?$/.test(path))) return "user.deployments"
  if (context?.kind === "pulls") return "auth.pullRequestJobs"
  if (context?.kind === "runs") return "auth.workflowRunJobs"
  if (context?.kind === "branches") return "auth.branchJobs"
  if (returnTo.startsWith("/github/pulls/")) return "auth.pullRequestJobs"
  if (returnTo.startsWith("/github/runs/")) return "auth.workflowRunJobs"
  if (returnTo.startsWith("/github/branches/")) return "auth.branchJobs"
  if (returnTo === "/repositories") return "auth.repositories"
  if (returnTo.startsWith("/account/") || returnTo.startsWith("/organizations/")) return "auth.accountSettings"
  if (returnTo.startsWith("/admin")) return "auth.adminConsole"
  if (returnTo !== "/jobs" && returnTo.startsWith("/jobs/")) return "auth.jobDetails"
  return "auth.jobs"
}
