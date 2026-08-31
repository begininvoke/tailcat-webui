# Spec: operations-console

## Objective

Add diagnostics, policy and transfer workflows to the existing responsive Ant
Design console while preserving five top-level mobile destinations.

## Tech Stack

React 19, TypeScript 6, React Router 7, Ant Design 6, i18next and the existing
CSS/token system. No chart or motion dependency is added.

## Commands

- Lint: `cd web && pnpm lint`
- Test: `cd web && pnpm test`
- Build: `cd web && pnpm build`
- Browser QA: embedded production binary at 1440, 768, 390 and 320 pixels.

## Project Structure

- `web/src/components/OperationProgress.tsx`: shared progress/status display.
- `web/src/components/RuntimeState.tsx`: exhaustive phase mapping.
- `web/src/pages/ClientsPage.tsx`: Clients and Diagnostics tabs.
- `web/src/pages/RoutesPage.tsx`: Routes and Transfers tabs.
- `web/src/pages/ServersPage.tsx`: Mappings, allowlist and exit-policy tabs.
- `web/src/services/api.ts`: generated-equivalent typed contracts.

## Code Style

```tsx
<Progress
  percent={percent}
  status={phase === 'failed' ? 'exception' : 'active'}
  aria-label={t('operations.progressLabel', { percent })}
/>
```

Keep feature orchestration in pages, extract reused progress/status rendering,
use semantic Ant controls and mirror API unions without `any` or browser-native
dialogs.

## Design Direction

- Visual thesis: calm, technical and trustworthy, using the existing Tailcat
  teal signal color, flat surface steps and dense Swiss hierarchy.
- Content plan: orient with page/tab title, show current status and limits, then
  expose one primary action and compact history/detail.
- Interaction thesis: frequent progress updates are instant; Ant drawers and
  confirmations retain existing motion; progress changes use color/text and no
  decorative movement.
- CSS strategy: existing global CSS plus Ant Design component tokens only.

## Testing Strategy

- Vitest uses accessible roles/labels for tabs, progress, forms and cancellation.
- All copy exists in English and Simplified Chinese.
- Browser QA covers light/dark/system, keyboard focus, reduced motion, 320/390
  mobile, desktop, long paths and zero axe violations.

## Boundaries

- Always: Ant components, inline validation, 44px mobile targets, tabular
  numerals, progress text in addition to color, and cancel for long operations.
- Ask first: none.
- Never: add a sixth bottom-navigation item, browser alert/confirm/prompt,
  decorative charts, fake metrics, looping animation or unlocalized errors.

## Success Criteria

- Diagnostics and Transfers are discoverable without changing primary nav count.
- Every long operation exposes phase, byte/file progress, cancel and recovery.
- Desktop uses comparison tables where useful; mobile uses cards/lists without
  horizontal scrolling.
- Existing brand, light/dark themes and screenshots remain coherent.

## Open Questions

None. Clients owns Diagnostics; Published routes owns Transfers.
