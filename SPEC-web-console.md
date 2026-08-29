# Spec: web-console

## Objective

Deliver a responsive network console that feels calm and immediate on desktop
and mobile, uses framework components consistently, and is fully usable in
English or Simplified Chinese and light or dark appearance.

## Tech Stack

React 19, TypeScript, Vite, React Router 7, Ant Design 6 and Ant Design Icons.
Ant Design supplies navigation, forms, tables, cards, dialogs, drawers,
feedback and accessibility behavior.

## Commands

```sh
cd web && pnpm install --frozen-lockfile
cd web && pnpm lint && pnpm test && pnpm build
```

## Project Structure

```text
web/src/app/          providers, routing, theme and locale
web/src/features/     dashboard, servers, clients, routes and settings
web/src/components/   reusable composed Ant Design components
web/src/services/     typed API and event clients
```

## Code Style

```tsx
export function RuntimeStatus({ status }: RuntimeStatusProps) {
  return <Badge status={statusToBadge(status)} text={statusLabel(status)} />;
}
```

Feature folders own their screens and forms. Repeated domain presentation is
extracted into typed components; no generic component is extracted for a
single use.

## Testing Strategy

Vitest and Testing Library cover theme/locale persistence, navigation, forms,
API errors and key workflows. Browser checks cover 390, 768, 1024 and 1440 px,
keyboard navigation, contrast and reduced motion.

## Boundaries

- Always: semantic Ant controls, visible focus, 44px mobile targets, localized
  strings, system theme support, empty/loading/error states and deep links.
  Confirmations, editors and feedback use Ant Design Modal, Drawer, Popconfirm
  and App notification components.
- Ask first: introduce another component library or animation dependency.
- Never: raw clickable divs, emoji icons, auth tokens in localStorage,
  browser `alert`/`confirm`/`prompt`, `transition: all`, `scale(0)` entrances or
  motion on keyboard navigation.

## Success Criteria

- Desktop uses a compact sidebar; mobile uses a safe-area-aware bottom nav.
- Theme offers light, dark and system modes with no initial flash.
- Language offers English and Chinese, follows the browser initially, and is
  persisted independently from the session.
- Screens exist for overview, servers, clients, published routes and settings.
- Occasional drawers/modals use transform+opacity under 250ms; button press
  feedback uses 120ms; reduced motion becomes an opacity-only transition.
