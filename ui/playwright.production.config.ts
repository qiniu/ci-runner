import { defineConfig } from "@playwright/test"

const port = process.env.RUNNERD_UI_SMOKE_PORT ?? "4173"
const externalBaseURL = process.env.RUNNERD_UI_SMOKE_BASE_URL
const baseURL = externalBaseURL ?? `http://127.0.0.1:${port}`

export default defineConfig({
  testDir: "./e2e",
  testMatch: ["production-smoke.pw.ts", "admin-request-loading.pw.ts"],
  fullyParallel: false,
  workers: 1,
  reporter: process.env.CI ? "line" : "list",
  use: {
    baseURL,
    trace: "retain-on-failure",
  },
  projects: [
    {
      name: "chromium",
      use: {
        browserName: "chromium",
      },
    },
  ],
  webServer: externalBaseURL
    ? undefined
    : {
        command: `bun run build && bun run preview --host 127.0.0.1 --port ${port} --strictPort`,
        url: baseURL,
        reuseExistingServer: false,
        timeout: 120_000,
      },
})
