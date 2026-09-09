# Development Workflow

## Start Here

- Run `git status --short --branch` before editing and preserve user-owned changes.
- Read the exact code, scripts, tests, or docs that back the requested behavior before changing documents.
- Keep changes scoped. Do not edit generated UI assets in `internal/server/ui/` by hand; edit `ui/` and rebuild with `task build` or `task ui-build`.
- Do not commit real secrets, local sqlite databases, `runnerd.local.yaml`, `.smee-url`, private keys, cookie jars, or deployment-local hostnames.

## Project Commands

Common commands:

```bash
task deps
task ui-deps
task ui-i18n-check
task dev
task smee
task lint
task test
task build
task docker-check
task release-check
```

- `task dev` is the local development entrypoint. It defaults to `RUNNERD_CONFIG=runnerd.local.yaml`, starts Vite on the first available localhost port at or after `5173`, starts smee forwarding when `.smee-url` exists, and runs runnerd with the `development` build tag.
- `task smee` reads `.smee-url` and defaults to `SMEE_TARGET=http://127.0.0.1:25500/webhooks/github`.
- `task build` rebuilds production UI assets into `internal/server/ui/` before compiling `bin/runnerd`.
- `task ui-i18n-check` validates the English/Chinese resource contract, typed i18next keys, and untranslated visible literals before UI copy changes are handed off.

## Documentation Sync

- `README.md` and `README.zh.md` are the paired operator quick starts and should describe current product behavior, setup, run, build, and current limits.
- `docs/testing.md` and `docs/zh/testing.md` are the paired detailed local/GitHub setup and troubleshooting guides.
- `docs/deployment-smoke.md` and `docs/zh/deployment-smoke.md` are the paired production-style smoke checklists.
- `docs/runner-architecture-comparison.md` and `docs/runner-implementation-review.md`, plus their `docs/zh/` counterparts, are architecture/status documents, not agent rulebooks.
- `TODO.md` tracks unresolved product and operations decisions.
- Keep active work linked from `TODO.md`; removed implementation plans are not an active roadmap.
- `.agents/rules/` stores durable agent-only rules; `.agents/skills/` stores repeatable project-local workflows.

When changing config, build, development, deployment, public APIs, authentication/authorization, state semantics, or UI asset workflows, keep the English/Chinese README and `docs/` pairs, `TODO.md`, `AGENTS.md`, and relevant `.agents/` files aligned.

## Git

- Unless the user asks, do not commit automatically.
- If the user asks to commit, stage only the intended files and use a concise Angular-style message such as `docs: add agent rules`.
- Never use destructive commands such as `git reset --hard` or `git checkout --` to discard user work unless explicitly requested.
