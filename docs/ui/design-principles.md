# CHUZI UI Design Principles

## Product intent

CHUZI should feel quiet, mature, precise, and easy to operate. It may have
Apple-level interaction quality, but it must remain a CHUZI product with
Windows-native behavior and Semcosm identity.

## Principles

### Session-first

The first useful answer after launch is which Sessions exist, what state each
is in, and what the user can do next. Core readiness remains available, but it
is supporting context rather than the primary dashboard.

### Context-driven

Actions depend on the selected object and its current Core state. Show one
primary action, at most two supporting actions, and a More menu for uncommon or
dangerous actions. Do not render every possible operation simultaneously.

### Material-based

Use a small number of spatial materials: Window, Sidebar, Content Surface,
Inspector, Sheet, Overlay, and Workspace HUD. A bordered card is an occasional
surface, not the default container for every line of information.

### Progressive disclosure

The list answers what exists and what needs attention. The Inspector answers
why and what can happen next. Sheets and dialogs are reserved for edits,
confirmation, and potentially destructive actions.

### Domain truth stays in Core

The UI consumes redacted projections. It does not decide account state, retry,
lease, queue order, credentials, or browser success. Labels such as Running,
Queued, Failed, and Idle must map to explicit Core values and include an
unknown/unavailable path.

### Continuity over navigation

Selecting an object updates the Inspector in place. Opening a workspace is a
deliberate spatial transition; ordinary details must not open a new page or
modal dialog.

### Calm density

Use enough density to manage many Sessions, but preserve readable hierarchy and
breathing room. Avoid giant KPI numbers, saturated badges, gradients, neon,
permanent glass blur, and decorative animation.

### Native usefulness

Keyboard navigation, focus visibility, hit targets, high-contrast behavior,
window resizing, and system theme integration are product requirements. Visual
minimalism must not remove practical desktop affordances.

## Prohibited patterns

- Admin dashboard/KPI layout.
- Web-style page navigation as the only way to inspect an object.
- Card inside card inside card.
- All actions as equally prominent buttons.
- Full-window blur or glass used as decoration.
- macOS clone, copied Apple symbols, or Apple branding.
- UI-only business state or production-looking fake data.
