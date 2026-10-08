# Platform Runner Specs

The database is the authority for platform Runner Specs. Administrators maintain every spec at `/admin/runner_specs`; startup does not seed or reconcile a code-owned catalog. A fresh installation starts with an empty catalog. Restore a database backup or create specs in Admin before submitting workflows.

## Template binding and publication

| Field | Contract |
| --- | --- |
| `template_source` | `public` binds a stable public name; `private` binds an explicit physical ID. |
| `default_template_name` | Required for a public binding; resolved independently through each request's effective Sandbox region. |
| `template_id` | Required for a private binding; empty for public bindings. |
| `published` | Explicit opt-in to the public directory; private bindings cannot publish. |

New specs default to a private binding outside the public directory. Admin adds template binding and directory settings only, with no new Runner preparation, Docker or sponsorship controls. Public-name specs keep former managed behavior: use the preinstalled Runner, require Docker readiness, and apply the existing organization sponsorship authorization. Private-ID specs keep former custom behavior: official pre-registration Runner checks/update, best-effort Docker, and no sponsorship. Both keep official Runner self-update; public visibility does not grant credential access.

Creating a spec, changing its binding, publishing it or re-enabling a published spec validates with the configured **admin** Sandbox endpoint/key, even when runtime fallback is disabled. Public names must resolve uniquely to a public template in `ready` or `uploaded` state. Private IDs retain the existing access/effective-default-build check. Local validation runs first; provider I/O has a five-second total deadline and stays outside the audited transaction. Rejected/stale saves have no profile or audit changes.

`/runner-specs` shows enabled, published public specs only. `/user/runner-specs` returns published public entries as `platform_public`, plus the selected scope's own `scoped_custom` entries; unpublished/private platform specs are omitted. Only scoped custom entries expose physical IDs and GitHub Runner Groups. `GET /api/public/runner-templates` groups enabled, published public specs by stable name and exposes only names and workflow labels; the public projection is cached in each server process for 60 seconds, with concurrent cache fills coalesced. Successful Admin spec saves and deletes invalidate that process cache; browser caches may retain responses for 60 seconds. Provider catalogs remain separate credential-bound resources. Large specs may be published after configuring and validating a public-name binding; they are never included merely because of their names.

## Edits and compatibility

Names remain stable resource identifiers. Admin can edit labels, required labels, template binding, GitHub Runner Group, priority, enabled state, capacity and directory settings. Matching retains `required_labels ⊆ job_labels ⊆ labels` and scoped overrides. Execution-setting changes and deletion return `409 runner_spec_in_use` while queued/creating/running/stopping requests use that global spec; capacity, enabled state and publication may still change. CAS rejects stale writes. Mutations and audit events commit atomically.

The additive schema migration translates each row without a template source once, preserving labels, names, operator controls, timestamps and indexes. Old managed rows with public names retain publication, preinstalled preparation, Docker and sponsorship; their obsolete physical template ID is cleared. Old private rows remain unpublished with official preparation; any unused public template name is cleared so unrelated edits remain valid, while the physical template ID is preserved. Incomplete historical managed rows remain public-bound and unpublished; they are never silently converted into runnable private specs. Existing `managed_by` and `catalog_revision` columns/values are retained as inert compatibility metadata. Historical empty/global request sources and saved Sandbox snapshots remain valid. Later edits and deletes survive restart; the migration neither creates missing specs nor automatically promotes private large specs.

Back up the database before upgrading. Older binaries do not understand the new directory fields and may restore code-owned defaults, so downgrade behavior needs separate verification. The `runner_update_policy`, `require_docker` and `fork_sponsorship` columns from unpublished development builds are no longer created or used; existing columns and values remain intact. The separate retired `runner_profile_scope_controls` table also remains intact and inert if present. Changing a public alias requires workflow and regional runtime smoke, since validation proves provider metadata rather than image contents. Image building, pinning and publication stay in the template release workflow.
