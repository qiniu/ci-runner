# Frontend UI Consistency

These rules are mandatory for new and changed UI on ordinary-user and admin
pages. Explicit user requirements take precedence. Existing inconsistent code
is not permission to introduce another variant; keep unrelated cleanup outside
the current task.

## Reuse Before Styling

- Before implementing, inspect `ui/src/components/ui/` and an existing page
  with the same interaction. Identify the component and reference surface in
  the implementation or handoff description.
- Use the shared `Button`, `Input`, `Label`, `Select`, `Switch`, `ToggleGroup`, `Tabs`,
  `Dialog`, `Sheet`, `Tooltip`, `Card`, `Badge`, and `Table` where applicable.
  Compose existing primitives before introducing a new reusable component.
- Form dropdowns must use `Select`, `SelectTrigger`, `SelectValue`,
  `SelectContent`, and `SelectItem` from `@/components/ui/select`. Do not add
  native `<select>` controls or recreate them with page-specific CSS. Use
  `DropdownMenu` for action menus, not as a substitute for a form field.
- Do not replace available shared controls with styled native buttons/inputs
  or clickable `div`/`span` elements. Native elements inside a shared primitive,
  hidden form bridges, and specialized controls without a suitable primitive
  are valid; preserve their semantic and keyboard behavior.
- When a primitive lacks a required capability, prefer a compatible shared
  extension or reusable composition. A specialized control must have a concrete
  functional reason documented in the change; personal visual preference is
  not a reason to create another component style or add a UI library.

## Visual And Interaction Contract

- Use existing theme tokens and component sizes/variants. Do not independently
  redefine borders, radii, shadows, colors, control heights, or focus rings on
  individual pages. Page classes may control layout and width; a visual variant
  needed across pages belongs in the shared component.
- Follow comparable pages for spacing, headings, action placement, sidebar
  structure, and scroll ownership. Avoid redundant card titles and nested
  borders around the same content.
- Use the existing font tokens and Lucide icons. Preserve intentionally
  distinct log/terminal typography; do not add page-specific font families.
- Preserve labels, accessible names, keyboard navigation, focus restoration,
  selected/hover/disabled states, and loading/empty/error feedback. Changing
  the component must not clear selections, pinned commits, or async scope
  isolation.
- Inspect both normal and long values on desktop and narrow screens. Controls
  and popups must remain usable without causing horizontal page overflow.

## Reference Surfaces

| Pattern | Reference |
| --- | --- |
| Form dropdown | Sandbox service region selector (`sandbox-api-region`) in `ui/src/components/user-dashboard.tsx` |
| Deployment creation dropdowns | `ui/src/components/user-deployment-create-section.tsx` |
| Segmented list filters | Shared `ToggleGroup` / `ToggleGroupItem` in `ui/src/components/ui/toggle-group.tsx`; compact muted container with a raised selected segment, matching the Runner event filter. Deployment status uses `type="single"` and retains exactly one selection |
| Underlined workspace tabs | Shared `TabsList` / `TabsTrigger` with `variant="line"` in `ui/src/components/ui/tabs.tsx`; used by Jobs and deployment details |
| Inline detail fields | Shared `DetailField` in `ui/src/components/ui/detail-field.tsx`; used by Jobs and deployment resources, with a three-column desktop grid and single-column narrow layout |
| Protected list/detail layout | Jobs in `ui/src/components/user-dashboard.tsx` and `ui/src/components/runner-job-detail.tsx` |
| Deployment repository workspace | `ui/src/components/deployment-navigation.tsx`; use shared Collapsible/Button/AppLink, repository links separate from expand controls, and Jobs sidebar sizing/scroll ownership |
| Runner event rows | `ui/src/components/runner-event-timeline.tsx`; deployment logs retain their existing font |

Reference the current shared implementation, not a copied snapshot. User-approved
differences such as log typography remain intentional exceptions.

## Required Acceptance

- Inspect the changed code for duplicated primitives, native form dropdowns,
  and page-only overrides of shared visual styles. Resolve violations within
  the changed surface before declaring the UI work complete.
- Run checks appropriate to the change under
  [Testing And Verification](testing-and-verification.md). Tests passing does
  not establish visual consistency.
- For appearance or control-interaction changes, inspect the rendered result
  in a browser at desktop and narrow widths, including the open popup/dialog
  when applicable. Compare with the reference surface; check keyboard focus,
  hover/selection, long content, and changed loading/empty/error states.
- Report the reused components, reference surface, checks, and browser evidence.
  Distinguish fixture verification from a live service. If visual verification
  cannot run, state the remaining gap rather than claiming visual acceptance.

This is a mandatory implementation and review contract; it is not an automated
visual-diff or lint gate. Do not present it as CI enforcement.
