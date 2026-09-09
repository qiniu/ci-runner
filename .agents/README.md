# Agent Assets

This directory contains project-local guidance for future agents.

## Rules

- `rules/development-workflow.md`: repository workflow, command selection, generated-file boundaries, and documentation sync expectations.
- `rules/project-architecture.md`: durable runnerd architecture facts, ordinary-user/admin UI boundaries, and implementation constraints.
- `rules/testing-and-verification.md`: verification matrix for docs, state schema, UI, build, Docker, release, and deployment smoke work.
- `rules/frontend-internationalization.md`: durable i18n boundaries, language-switcher placement, stable identifier rules, and UI verification.

## Skills

- `skills/runnerd-state-schema/SKILL.md`: use when changing state records, GORM tags, indexes, or schema migration compatibility.
- `skills/runnerd-dev-smoke/SKILL.md`: use when validating `task dev`, Vite proxy behavior, smee forwarding, or local startup fixes.

Keep user/operator docs in `README.md` and `docs/`. Put agent-only durable rules and repeatable agent workflows here.

Treat `TODO.md` as the active roadmap.
