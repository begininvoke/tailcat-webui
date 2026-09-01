# Tailcat WebUI Design System

This file is the visual source of truth. Page files under `pages/` may override
it. Searchable UI guidance was used as a baseline, then reconciled with Ant
Design, Emil-style motion rules and Apple-style platform behavior.

## Product character

Calm, technical and trustworthy: a dense network operations console, not a
marketing dashboard. Use restrained Swiss hierarchy, precise alignment and
one teal signal color. Avoid gradients, glass cards, decorative glow, oversized
rounding and invented metrics.

## Framework

- Compose interaction from Ant Design 6 components. Do not hand-roll dialogs,
  drawers, menus, select controls, tables, notifications or confirmations.
- Use Ant Design Icons only. No emoji icons.
- Use React Router for deep links and route-level lazy loading.
- Never call browser `alert`, `confirm` or `prompt`.

## Appearance

Ant Design token overrides:

| Token | Light | Dark |
| --- | --- | --- |
| `colorPrimary` | `#00656F` | `#42C4CF` |
| `colorBgBase` | `#F4F6F5` | `#0E1213` |
| `colorBgContainer` | `#FFFFFF` | `#161B1D` |
| `colorTextBase` | `#152022` | `#EEF4F3` |
| `colorTextSecondary` | `#526164` | `#A9B6B8` |
| `colorBorder` | `#D9E0DE` | `#30383A` |
| `colorSuccess` | `#298B62` | `#58C995` |
| `colorWarning` | `#B66A16` | `#E3A14B` |
| `colorError` | `#C23B3B` | `#FF8F8F` |

Dark solid Button and Radio controls use component-level black text rather
than white so labels remain at WCAG AA contrast on the lighter dark-theme
primary, hover and pressed surfaces without changing Tooltip or Tag
foregrounds.
Selected framework controls use `#DFECEB`/`#D2E4E2` in light mode and
`#153638`/`#1D474A` in dark mode so dropdowns and menus retain AA contrast.

- Corner radius: 8px controls, 12px large surfaces. Pills only for tags/status.
- Shadows: none on ordinary cards; one subtle shadow for floating layers.
- Typography: system UI stack for prose and labels; `ui-monospace` only for
  tokens, addresses, ports and timings. Large headings tighten tracking to
  `-0.02em`; body stays at 16px/1.5.
- Desktop density is compact. Mobile touch targets are at least 44px with 8px
  separation and safe-area padding.

## Layout

- Desktop ≥1024px: 224px collapsible sidebar, floating top bar, content max
  width 1440px. Use tables where comparison matters and cards for summaries.
- Tablet: collapsed sidebar plus full-width content.
- Mobile <768px: no sidebar; fixed bottom navigation with at most five items,
  compact header, cards instead of horizontally scrolling tables.
- Keep the five primary destinations stable. Add related operational features
  as internal Ant Tabs within their owning page, such as Diagnostics under
  Clients and Transfers under Published routes.
- At 768px and wider, use Ant Table where comparison matters. Below 768px, use
  Ant List and cards with wrapping paths and no horizontal page scroll.
- Every page answers where the user is, what is running and what can be done.

## Theme and locale

- Appearance choices: light, dark, system. Default to system and persist the
  explicit choice. Prevent first-paint theme flash.
- Languages: English and Simplified Chinese. Default from browser language and
  persist the explicit choice. Switch Ant Design locale at the same time.
- Machine error codes remain stable English identifiers; user-facing copy is
  localized.

## Motion

Motion is sparse and never blocks work.

- Frequent navigation and keyboard actions: instant, no transition.
- Press feedback: `transform: scale(0.98)`, 120ms, strong ease-out.
- Hover color/border: 150ms `ease`, only under `(hover: hover) and
  (pointer: fine)`; never animate layout or add a translating card hover.
- Modal/drawer state: use Ant Design behavior, capped at 240ms. Entrances start
  at scale 0.97 or the correct edge, never scale 0.
- Runtime status changes: 160ms opacity/color transition; no looping pulse.
- Progress bars never use active stripes, looping animation or animated width.
  Pair every progress value with status text and tabular byte/file counts.
- Use only transform and opacity for custom motion. Name transition properties;
  `transition: all` is forbidden.
- Under `prefers-reduced-motion: reduce`, remove position/scale movement and
  retain a 120ms opacity/color transition.
- Under `prefers-reduced-transparency: reduce`, floating chrome becomes opaque.

Curves:

```css
--ease-out: cubic-bezier(0.23, 1, 0.32, 1);
--ease-in-out: cubic-bezier(0.77, 0, 0.175, 1);
--ease-drawer: cubic-bezier(0.32, 0.72, 0, 1);
```

## Accessibility and delivery gate

- WCAG AA text contrast, visible focus and accessible names for icon buttons.
- Native button semantics through Ant components; no clickable `div`.
- Forms use visible labels, inline errors and focus the first invalid field.
- Each operations page owns one visually hidden, polite live announcer. Shared
  progress components do not create competing live regions.
- One-time codes appear only after create or rotate, with explicit copy and
  dismiss actions. Rotation replaces the prior value; history tables never
  render capability material.
- Test keyboard navigation and layouts at 390, 768, 1024 and 1440px.
- README screenshots come from the running application in desktop light mode
  and mobile dark mode.
