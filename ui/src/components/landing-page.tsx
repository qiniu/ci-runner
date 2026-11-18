import {
  ArrowRight,
  BookOpen,
  Check,
  ChevronRight,
  CircleDot,
  Github,
  GitPullRequest,
  Layers3,
  Moon,
  ShieldCheck,
  Sparkles,
  Sun,
  Terminal,
  Trash2,
  Workflow,
  Zap,
} from "lucide-react"
import { useTheme } from "next-themes"
import { useTranslation } from "react-i18next"

import { LanguageSwitcher } from "@/components/language-switcher"
import { RunnerLifecyclePreview } from "@/components/landing-lifecycle-preview"
import { deploymentRequirementItems, landingChecklistItems } from "@/components/landing-content-utils"
import { QiniuRunnerLogo } from "@/components/qiniu-runner-logo"

const repositoryURL = "https://github.com/qiniu/ci-runner"
const documentationURL = "/docs"
const hostedGuideURL = "/docs/getting-started/hosted"
const deploymentGuideURL = "/docs/getting-started/deploy"
const licenseURL = `${repositoryURL}/blob/main/LICENSE`

const capabilities = [
  {
    id: "isolated",
    icon: Layers3,
  },
  {
    id: "justInTime",
    icon: Zap,
  },
  {
    id: "lifecycle",
    icon: ShieldCheck,
  },
] as const

const workflowSteps = [
  {
    id: "queued",
    number: "01",
    icon: GitPullRequest,
  },
  {
    id: "matched",
    number: "02",
    icon: Workflow,
  },
  {
    id: "running",
    number: "03",
    icon: Terminal,
  },
  {
    id: "cleared",
    number: "04",
    icon: Trash2,
  },
] as const

export function LandingPage() {
  const { t, i18n } = useTranslation()
  const chinese = i18n.resolvedLanguage === "zh"
  return (
    <div className="min-h-screen overflow-x-hidden bg-white text-[#0a0d12] transition-colors duration-300 dark:bg-[#050d12] dark:text-[#edf8fc]">
      <a
        href="#main-content"
        className="fixed left-4 top-4 z-[60] -translate-y-24 rounded-md bg-[#0a0d12] px-4 py-2 text-sm font-semibold text-white transition-transform focus:translate-y-0"
      >
        {t("landing.skipToContent")}
      </a>

      <header className="absolute inset-x-0 top-0 z-50 border-b border-white/10">
        <div className="mx-auto flex h-20 max-w-[1440px] items-center justify-between px-5 sm:px-8 lg:px-12">
          <a href="/" className="group" aria-label={t("common.productHome")}>
            <QiniuRunnerLogo inverse />
          </a>

          <nav className="hidden items-center gap-8 text-[15px] text-white/70 md:flex" aria-label={t("landing.primaryNavigation")}>
            <a className="transition-colors hover:text-white" href="#capabilities">
              {t("landing.product")}
            </a>
            <a className="transition-colors hover:text-white" href="#open-source">
              {t("landing.openSource")}
            </a>
            <a className="transition-colors hover:text-white" href="#how-it-works">
              {t("landing.howItWorks")}
            </a>
            <a
              className="inline-flex items-center gap-1.5 transition-colors hover:text-white"
              href={documentationURL}
            >
              {t("landing.documentation")}
              <ArrowRight className="h-3.5 w-3.5 -rotate-45" />
            </a>
          </nav>

          <div className="flex items-center gap-2">
            <a
              href={documentationURL}
              aria-label={t("landing.documentation")}
              className="inline-flex h-10 w-10 items-center justify-center rounded-md border border-white/20 text-white transition-colors hover:bg-white/10 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[#27c5f5] md:hidden"
            >
              <BookOpen className="h-4 w-4" />
            </a>
            <LandingThemeToggle />
            <LanguageSwitcher className="text-white hover:bg-white/10 hover:text-white" />
            <LandingAccessAction compact />
          </div>
        </div>
      </header>

      <main id="main-content" tabIndex={-1}>
        <section className="relative isolate min-h-[760px] overflow-hidden bg-[#07131b] pb-20 pt-28 sm:pt-32 lg:min-h-[760px] lg:pb-24 lg:pt-36">
        <div className="absolute inset-0 -z-30 bg-[linear-gradient(112deg,#07131b_0%,#092433_54%,#06324a_100%)]" />
        <div className="absolute -right-40 -top-48 -z-20 h-[680px] w-[680px] rounded-full bg-[#00aae7]/20 blur-[110px]" />
        <div className="absolute -bottom-72 -left-32 -z-20 h-[620px] w-[620px] rounded-full bg-[#0088c2]/20 blur-[130px]" />
        <div className="brand-grid absolute inset-0 -z-10 opacity-50" />

        <div className="mx-auto grid max-w-[1440px] items-center gap-16 px-5 sm:px-8 lg:grid-cols-[0.92fr_1.08fr] lg:gap-20 lg:px-12">
          <div className="brand-reveal max-w-2xl">
            <div className="mb-7 inline-flex items-center gap-2 rounded-full border border-[#27c5f5]/30 bg-[#00aae7]/10 px-3.5 py-2 font-mono text-[11px] uppercase tracking-[0.16em] text-[#7ddcff]">
              <Sparkles className="h-3.5 w-3.5" />
              {t("landing.ephemeralInfrastructure")}
            </div>

            <h1
              aria-label={t("landing.heroLabel")}
              className={`max-w-[760px] font-semibold text-white ${chinese
                ? "text-[clamp(1.5rem,8vw,3.5rem)] leading-[1.06] tracking-tight lg:text-[clamp(2.25rem,3.5vw,4rem)]"
                : "text-[clamp(3rem,5.4vw,5.8rem)] leading-[0.94] tracking-[-0.065em]"}`}
            >
              {t("landing.heroPrefix")}
              {chinese ? (
                <span className="mt-2 block text-balance break-keep">
                  <span className="text-[#27c5f5]">{t("landing.heroPoweredBy")}</span>{"\u00a0"}{t("landing.heroSandbox")}
                </span>
              ) : (
                <>
                  <span className="mt-2 block text-[#27c5f5]">{t("landing.heroPoweredBy")}</span>
                  <span className="mt-2 block">{t("landing.heroSandbox")}</span>
                </>
              )}
            </h1>

            <p className="mt-8 max-w-xl text-base leading-7 text-[#c5d7e1] sm:text-lg sm:leading-8">
              {t("landing.heroDescription")}
            </p>

            <div className="mt-10 flex flex-col gap-3 sm:flex-row sm:items-center">
              <LandingAccessAction href={hostedGuideURL} />
              <a
                href="#how-it-works"
                className="group inline-flex h-12 items-center justify-center gap-2 rounded-md border border-white/20 px-5 text-sm font-semibold text-white transition-all hover:border-white/40 hover:bg-white/5 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[#27c5f5]"
              >
                {t("landing.seeHowItWorks")}
                <ChevronRight className="h-4 w-4 transition-transform group-hover:translate-x-0.5" />
              </a>
            </div>

            <div className="mt-9 flex flex-wrap gap-x-6 gap-y-3 text-xs text-white/55 sm:text-sm">
              {landingChecklistItems(t).map((item) => (
                <span key={item.id} className="inline-flex items-center gap-2">
                  <Check className="h-3.5 w-3.5 text-[#27c5f5]" />
                  {item.label}
                </span>
              ))}
            </div>
          </div>

          <RunnerLifecyclePreview />
        </div>

        <div className="pointer-events-none absolute bottom-0 left-1/2 h-20 w-px bg-gradient-to-b from-[#27c5f5]/0 to-[#27c5f5]/70" />
      </section>

      <section className="border-b border-[#e5edf4] bg-[#f8fcfe] transition-colors dark:border-white/10 dark:bg-[#07131b]">
        <div className="mx-auto grid max-w-[1440px] divide-y divide-[#e5edf4] px-5 sm:px-8 md:grid-cols-3 md:divide-x md:divide-y-0 lg:px-12 dark:divide-white/10">
          {[
            ["01", t("landing.lifecycle.githubSends")],
            ["02", t("landing.lifecycle.createsCapacity")],
            ["03", t("landing.lifecycle.executes")],
          ].map(([number, label]) => (
            <div key={number} className="flex items-center gap-4 py-5 md:px-6 first:md:pl-0">
              <span className="font-mono text-xs text-[#006b91] dark:text-[#7ddcff]">{number}</span>
              <span className="text-sm font-medium text-[#384b57] dark:text-[#b7ccd7]">{label}</span>
            </div>
          ))}
        </div>
      </section>

      <section id="capabilities" className="scroll-mt-8 bg-white py-24 transition-colors sm:py-32 dark:bg-[#050d12]">
        <div className="mx-auto max-w-[1440px] px-5 sm:px-8 lg:px-12">
          <SectionIntro
            label={t("landing.capabilitiesIntro")}
            title={t("landing.capabilitiesTitle")}
            description={t("landing.capabilitiesDescription")}
          />

          <div className="mt-14 grid gap-px overflow-hidden rounded-xl border border-[#e5edf4] bg-[#e5edf4] lg:grid-cols-3 dark:border-white/10 dark:bg-white/10">
            {capabilities.map(({ id, icon: Icon }, index) => (
              <article
                key={id}
                className="group relative min-h-[360px] overflow-hidden bg-white p-7 transition-colors hover:bg-[#f7fcff] sm:p-9 dark:bg-[#0b1e29] dark:hover:bg-[#0d2633]"
              >
                <div className="absolute right-0 top-0 font-mono text-[92px] font-semibold leading-none tracking-[-0.08em] text-[#f0f7fa] transition-colors group-hover:text-[#e7f7fc] dark:text-white/[0.035] dark:group-hover:text-[#102d3b]">
                  0{index + 1}
                </div>
                <div className="relative flex h-full flex-col">
                  <div className="flex h-12 w-12 items-center justify-center rounded-lg border border-[#bdeaf8] bg-[#f2fcff] text-[#006b91] dark:border-[#27c5f5]/20 dark:bg-[#00aae7]/10 dark:text-[#7ddcff]">
                    <Icon className="h-5 w-5" />
                  </div>
                  <p className="mt-14 font-mono text-[11px] font-semibold uppercase tracking-[0.16em] text-[#006b91] dark:text-[#7ddcff]">
                    {t(`landing.capabilities.${id}.eyebrow`)}
                  </p>
                  <h3 className="mt-4 text-2xl font-semibold tracking-[-0.035em] text-[#0a0d12] dark:text-[#edf8fc]">{t(`landing.capabilities.${id}.title`)}</h3>
                  <p className="mt-4 max-w-sm text-[15px] leading-7 text-[#596b75] dark:text-[#a9bfcb]">{t(`landing.capabilities.${id}.description`)}</p>
                  <div className="mt-auto flex items-center gap-2 border-t border-[#e5edf4] pt-6 text-xs font-medium text-[#384b57] dark:border-white/10 dark:text-[#b7ccd7]">
                    <CircleDot className="h-3.5 w-3.5 text-[#006b91] dark:text-[#7ddcff]" />
                    {t(`landing.capabilities.${id}.detail`)}
                  </div>
                </div>
              </article>
            ))}
          </div>
        </div>
      </section>

      <section
        id="open-source"
        aria-labelledby="open-foundation-title"
        className="scroll-mt-8 border-b border-[#dceaf1] bg-[#f8fcfe] py-16 transition-colors sm:py-20 dark:border-white/10 dark:bg-[#07131b]"
      >
        <div className="mx-auto max-w-[1440px] px-5 sm:px-8 lg:px-12">
          <div className="grid gap-6 lg:grid-cols-[0.75fr_1.25fr] lg:items-end">
            <div>
              <p className="font-mono text-[11px] font-semibold uppercase tracking-[0.18em] text-[#006b91] dark:text-[#7ddcff]">
                {t("landing.openSourceEyebrow")}
              </p>
              <h2
                id="open-foundation-title"
                className="mt-4 max-w-xl text-3xl font-semibold leading-[1.06] tracking-[-0.045em] text-[#0a0d12] sm:text-4xl dark:text-[#edf8fc]"
              >
                {t("landing.openSourceTitle")}
              </h2>
            </div>
            <p className="max-w-2xl text-base leading-7 text-[#596b75] lg:justify-self-end dark:text-[#a9bfcb]">
              {t("landing.openSourceDescription")}
            </p>
          </div>

          <div className="mt-10 grid overflow-hidden rounded-xl border border-[#cfe2eb] bg-[#cfe2eb] lg:grid-cols-[1.08fr_0.92fr] lg:gap-px dark:border-white/10 dark:bg-white/10">
            <article className="relative overflow-hidden bg-[#07131b] p-7 text-white sm:p-10 lg:p-12">
              <div
                aria-hidden="true"
                className="brand-grid pointer-events-none absolute inset-0 opacity-40"
              />
              <div className="relative">
                <div className="flex h-12 w-12 items-center justify-center rounded-lg border border-[#27c5f5]/35 bg-[#00aae7]/10 text-[#7ddcff]">
                  <Github className="h-5 w-5" />
                </div>
                <p className="mt-10 font-mono text-[11px] font-semibold uppercase tracking-[0.16em] text-[#7ddcff]">
                  {t("landing.openSourceCardEyebrow")}
                </p>
                <h3 className="mt-4 text-2xl font-semibold tracking-[-0.035em] sm:text-3xl">
                  {t("landing.openSourceCardTitle")}
                </h3>
                <p className="mt-4 max-w-xl text-[15px] leading-7 text-[#b7ccd7]">
                  {t("landing.openSourceCardDescription")}
                </p>
                <div className="mt-8 flex flex-wrap items-center gap-x-5 gap-y-3">
                  <a
                    href={repositoryURL}
                    target="_blank"
                    rel="noreferrer"
                    className="group inline-flex items-center gap-2 text-sm font-semibold text-[#7ddcff] transition-colors hover:text-white focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-[#27c5f5]"
                  >
                    {t("landing.viewSource")}
                    <ArrowRight className="h-4 w-4 transition-transform group-hover:translate-x-1" />
                  </a>
                  <a
                    href={licenseURL}
                    target="_blank"
                    rel="noreferrer"
                    className="border-l border-white/20 pl-5 text-sm font-medium text-[#b7ccd7] transition-colors hover:text-white focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-[#27c5f5]"
                  >
                    {t("landing.licensed")}
                  </a>
                </div>
              </div>
            </article>

            <article className="bg-white p-7 transition-colors sm:p-10 lg:p-12 dark:bg-[#0b1e29]">
              <div className="flex h-12 w-12 items-center justify-center rounded-lg border border-[#bdeaf8] bg-[#f2fcff] text-[#006b91] dark:border-[#27c5f5]/20 dark:bg-[#00aae7]/10 dark:text-[#7ddcff]">
                <Layers3 className="h-5 w-5" />
              </div>
              <p className="mt-10 font-mono text-[11px] font-semibold uppercase tracking-[0.16em] text-[#006b91] dark:text-[#7ddcff]">
                {t("landing.cloudServiceEyebrow")}
              </p>
              <h3 className="mt-4 text-2xl font-semibold tracking-[-0.035em] text-[#0a0d12] sm:text-3xl dark:text-[#edf8fc]">
                {t("landing.cloudServiceTitle")}
              </h3>
              <p className="mt-4 max-w-xl text-[15px] leading-7 text-[#596b75] dark:text-[#a9bfcb]">
                {t("landing.cloudServiceDescription")}
              </p>
            </article>
          </div>
        </div>
      </section>

      <section id="how-it-works" className="scroll-mt-8 overflow-hidden bg-[#081720] py-24 text-white sm:py-32">
        <div className="mx-auto max-w-[1440px] px-5 sm:px-8 lg:px-12">
          <div className="grid gap-12 lg:grid-cols-[0.7fr_1.3fr] lg:gap-20">
            <div>
              <p className="font-mono text-[11px] font-semibold uppercase tracking-[0.18em] text-[#27c5f5]">
                {t("landing.howItWorks")}
              </p>
              <h2 className="mt-5 max-w-lg text-4xl font-semibold leading-[1.04] tracking-[-0.05em] sm:text-5xl">
                {t("landing.howItWorksTitle")}
              </h2>
              <p className="mt-6 max-w-md text-base leading-7 text-[#a9bfcb]">
                {t("landing.howItWorksDescription")}
              </p>
              <a
                href={documentationURL}
                className="group mt-9 inline-flex items-center gap-2 text-sm font-semibold text-[#7ddcff] transition-colors hover:text-white"
              >
                {t("landing.readDocumentation")}
                <ArrowRight className="h-4 w-4 transition-transform group-hover:translate-x-1" />
              </a>
            </div>

            <div className="relative">
              <div className="absolute bottom-8 left-[41px] top-8 w-px -translate-x-1/2 bg-gradient-to-b from-[#27c5f5] via-[#00aae7]/35 to-transparent sm:left-[57px]" />
              <div className="space-y-3">
                {workflowSteps.map(({ id, number, icon: Icon }) => (
                  <article
                    key={number}
                    className="group relative grid grid-cols-[48px_1fr] gap-4 rounded-xl border border-white/10 bg-white/[0.035] p-4 transition-all hover:border-[#27c5f5]/35 hover:bg-[#00aae7]/[0.06] sm:grid-cols-[64px_1fr] sm:gap-6 sm:p-6"
                  >
                    <div className="relative z-10 flex h-12 w-12 items-center justify-center rounded-full border border-[#27c5f5]/35 bg-[#0a2634] text-[#7ddcff] sm:h-16 sm:w-16">
                      <Icon className="h-5 w-5 sm:h-6 sm:w-6" />
                    </div>
                    <div className="py-1">
                      <div className="flex items-center justify-between gap-4">
                        <h3 className="text-lg font-semibold tracking-[-0.02em] sm:text-xl">{t(`landing.workflow.${id}.title`)}</h3>
                        <span className="font-mono text-xs text-white/60">{number}</span>
                      </div>
                      <p className="mt-2 max-w-xl text-sm leading-6 text-[#9bb2be] sm:text-[15px]">{t(`landing.workflow.${id}.description`)}</p>
                    </div>
                  </article>
                ))}
              </div>
            </div>
          </div>
        </div>
      </section>

      <section className="relative overflow-hidden bg-[#00aae7] py-20 sm:py-24">
        <div className="brand-grid brand-grid-dark absolute inset-0 opacity-30" />
        <div className="relative mx-auto grid max-w-[1440px] gap-10 px-5 sm:px-8 lg:grid-cols-[1.08fr_0.92fr] lg:items-end lg:gap-16 lg:px-12">
          <div>
            <p className="font-mono text-[11px] font-semibold uppercase tracking-[0.18em] text-[#073149]">
              {t("landing.choosePath")}
            </p>
            <h2 className="mt-4 max-w-3xl text-4xl font-semibold leading-[1.02] tracking-[-0.055em] text-[#07131b] sm:text-5xl lg:text-6xl">
              {t("landing.choosePathTitle")}
            </h2>
            <p className="mt-6 max-w-3xl text-base leading-7 text-[#073149] sm:text-lg sm:leading-8">
              {t("landing.choosePathDescription")}
            </p>
          </div>
          <div className="rounded-xl border border-[#07131b]/15 bg-white/20 p-5 backdrop-blur-sm sm:p-6">
            <div className="flex flex-col gap-3 sm:flex-row">
              <LandingAccessAction href={hostedGuideURL} inverted label={t("landing.hosted")} />
              <a
                href={deploymentGuideURL}
                className="group inline-flex h-12 items-center justify-center gap-2.5 rounded-md border border-[#07131b]/25 bg-white/35 px-5 text-sm font-semibold text-[#07131b] transition-all hover:border-[#07131b]/45 hover:bg-white/55 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[#07131b]"
              >
                {t("landing.deploy")}
                <ArrowRight className="h-4 w-4 -rotate-45 transition-transform group-hover:translate-x-0.5 group-hover:-translate-y-0.5" />
              </a>
            </div>
            <p className="mt-5 text-xs font-semibold uppercase tracking-[0.12em] text-[#073149]">
              {t("landing.deploymentLabel")}
            </p>
            <div className="mt-3 flex flex-wrap gap-x-5 gap-y-2 text-sm text-[#073149]">
              {deploymentRequirementItems(t).map((item) => (
                <span key={item.id} className="inline-flex items-center gap-2">
                  <CircleDot className="h-3.5 w-3.5" />
                  {item.label}
                </span>
              ))}
            </div>
          </div>
        </div>
      </section>
      </main>

      <footer className="border-t border-[#e5edf4] bg-white transition-colors dark:border-white/10 dark:bg-[#050d12]">
        <div className="mx-auto flex max-w-[1440px] flex-col gap-6 px-5 py-8 text-sm text-[#596b75] sm:px-8 md:flex-row md:items-center md:justify-between lg:px-12 dark:text-[#a9bfcb]">
          <div className="flex items-center gap-4 text-[#0a0d12] dark:text-[#edf8fc]">
            <QiniuRunnerLogo />
            <span className="hidden h-8 w-px bg-[#d5e4eb] sm:block dark:bg-white/10" aria-hidden="true" />
            <div className="hidden text-xs text-[#596b75] sm:block dark:text-[#a9bfcb]">{t("landing.poweredBy")}</div>
          </div>
          <div className="flex flex-wrap items-center gap-x-6 gap-y-3">
            <a className="transition-colors hover:text-[#006b91]" href="#capabilities">
              {t("landing.product")}
            </a>
            <a className="transition-colors hover:text-[#006b91]" href="#how-it-works">
              {t("landing.howItWorks")}
            </a>
            <a
              className="inline-flex items-center gap-1.5 transition-colors hover:text-[#006b91]"
              href={documentationURL}
            >
              {t("landing.documentation")}
              <ArrowRight className="h-3.5 w-3.5 -rotate-45" />
            </a>
            <a
              className="inline-flex items-center gap-1.5 transition-colors hover:text-[#006b91]"
              href={repositoryURL}
              target="_blank"
              rel="noreferrer"
            >
              GitHub
              <Github className="h-3.5 w-3.5" />
            </a>
            <a
              className="transition-colors hover:text-[#006b91]"
              href={licenseURL}
              target="_blank"
              rel="noreferrer"
            >
              Apache-2.0
            </a>
          </div>
        </div>
      </footer>
    </div>
  )
}

function LandingThemeToggle() {
  const { resolvedTheme, setTheme } = useTheme()
  const { t } = useTranslation()
  const nextTheme = resolvedTheme === "dark" ? "light" : "dark"
  const label = nextTheme === "dark" ? t("landing.switchThemeDark") : t("landing.switchThemeLight")

  return (
    <button
      type="button"
      data-theme-toggle="landing"
      aria-label={label}
      title={label}
      onClick={() => setTheme(nextTheme)}
      className="inline-flex h-10 w-10 items-center justify-center rounded-md border border-white/20 text-white transition-all hover:border-white/40 hover:bg-white/10 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[#27c5f5]"
    >
      <Moon className="h-4 w-4 dark:hidden" />
      <Sun className="hidden h-4 w-4 dark:block" />
    </button>
  )
}

function LandingAccessAction({
  href = "/jobs",
  inverted = false,
  compact = false,
  label,
}: {
  href?: string
  inverted?: boolean
  compact?: boolean
  label?: string
}) {
  const { t } = useTranslation()
  const actionLabel = label ?? t("landing.getStarted")
  const colorClass = inverted
    ? "bg-[#07131b] text-white hover:bg-[#0d2533] focus-visible:outline-[#07131b]"
    : compact
      ? "bg-white text-[#0a0d12] hover:bg-[#eaf9fe] focus-visible:outline-[#27c5f5]"
      : "bg-[#00aae7] text-[#041018] hover:bg-[#27c5f5] focus-visible:outline-[#27c5f5]"
  const sizeClass = compact ? "h-10 px-4" : "h-12 px-5"

  return (
    <a
      href={href}
      aria-label={compact ? actionLabel : undefined}
      className={`group inline-flex items-center justify-center gap-2.5 rounded-md text-sm font-semibold transition-all focus-visible:outline-2 focus-visible:outline-offset-2 ${sizeClass} ${colorClass}`}
    >
      <span className={compact ? "hidden sm:inline" : undefined}>{actionLabel}</span>
      {compact ? <Terminal className="h-4 w-4 sm:hidden" /> : null}
      <ArrowRight className="h-4 w-4 transition-transform group-hover:translate-x-0.5" />
    </a>
  )
}

function SectionIntro({
  label,
  title,
  description,
}: {
  label: string
  title: string
  description: string
}) {
  return (
    <div className="grid gap-6 lg:grid-cols-[1fr_0.7fr] lg:items-end">
      <div>
        <p className="font-mono text-[11px] font-semibold uppercase tracking-[0.18em] text-[#006b91] dark:text-[#7ddcff]">{label}</p>
        <h2 className="mt-5 max-w-4xl text-4xl font-semibold leading-[1.04] tracking-[-0.05em] text-[#0a0d12] sm:text-5xl lg:text-6xl dark:text-[#edf8fc]">
          {title}
        </h2>
      </div>
      <p className="max-w-lg text-base leading-7 text-[#596b75] lg:justify-self-end dark:text-[#a9bfcb]">{description}</p>
    </div>
  )
}
