# Browser QA

Date: 2026-08-30
Target: embedded production build at `http://localhost:8080`
Browser: headless Chromium through agent-browser

## Coverage

- Demo authentication and secure-cookie session restoration
- Desktop navigation and every top-level page
- Server create/start/stop, live token and public-key presentation
- Client form validation, creation and successful Tailcat ping
- Route creation from an existing client
- Mobile bottom navigation and bottom-drawer forms
- English/Chinese switching and light/dark persistence
- Console errors and uncaught page errors
- WCAG 2 A/AA, 2.1 AA and 2.2 AA axe scans

## Resolved findings

1. Long connection tokens expanded the Ant Descriptions table beyond its card.
   Fixed with fixed table layout and a constrained, copyable monospace value.
2. `Status().Self` exposed a zero-value server key with the pinned Tailcat
   revision. Fixed by parsing the public key from the freshly generated,
   verified connection token.
3. Ant Design derived secondary colors missed AA contrast in light and dark
   themes. Fixed with explicit semantic text/menu/link tokens.
4. A color/background transition briefly passed through a low-contrast state
   when the Publish button changed from disabled to enabled. Button base-state
   transitions now animate only press transform; hover color transitions are
   pointer-gated.
5. Dark-theme primary and danger button labels, plus the shared selected-item
   background used by Ant Dropdown, fell below AA contrast on the published
   routes page. Component and global theme tokens now keep stable foreground /
   background pairs in both themes.
6. A first-pass global solid-text token would also have darkened Tooltip text.
   The override is now scoped to Ant Button and Radio only; an expanded dark
   Tooltip and the route-creation Drawer were scanned separately.
7. The first component-scoped dark foreground did not leave enough contrast
   margin on Ant's pressed primary/danger backgrounds. Solid Button and Radio
   foregrounds now use black in dark mode across normal, hover and active
   states.

## Final results

- Desktop light published-routes page, including the open language menu:
  **0 axe violations**.
- Mobile dark Chinese dashboard and published-routes page: **0 axe
  violations**.
- Mobile dark route-creation Drawer and an explicitly expanded Tooltip:
  **0 axe violations** after transition settle.
- One axe item remains `incomplete`: the single-letter avatar is too short for
  automated contrast inference. Its computed colors are white on `#3A4749`, a
  9.64:1 contrast ratio.
- Browser console: no JavaScript exceptions or uncaught page errors during the
  tested workflows.
- Browser-native dialog detection stayed enabled throughout the final smoke;
  no `alert`, `confirm`, or `prompt` dialog appeared.
- Pointer-down inspection measured black on `rgb(50, 134, 142)` for the dark
  primary active state (4.94:1) and black on `rgb(173, 100, 100)` for the dark
  danger active state (4.78:1).
