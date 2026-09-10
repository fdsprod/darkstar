# Component catalog

The dashboard separates presentation from orchestration in three layers. The
layer a file belongs to is decided by what it is allowed to import, not by its
name, and `tests/component-purity.test.mjs` fails when the boundary is crossed.

| Layer | Location | May import | Owns |
|---|---|---|---|
| Pure UI | `src/components/ui`, `src/components/document`, `src/components/terminal` | React, generated schema types, sibling pure components | Markup, callbacks, controlled state passed in as props |
| Wrapper | `src/pages/*Page.tsx`, `src/components/AppShell.tsx` | Anything | API reads and writes, routing, selection anchoring, revision orchestration |
| Pure logic | `src/pages/*Model.ts`, `src/components/*/[a-z]*Model.ts` | Nothing browser-specific | Deterministic derivation, validated under `node --test` without a DOM |

A pure component never imports the API client, `src/state`, or `src/app/router`.
Generated schema types carry no behavior and stay allowed. A pure component
receives resolved data and callbacks, so it renders in a story, in an acceptance
test, and in the application from the same props. `src/components/ui/Link.tsx`
is the router-free half of navigation: a wrapper supplies `onNavigate`.

`src/components/PageStructure.tsx` still imports `AppLink` and is therefore a
wrapper, not a pure component.

## Primitives

`ui/Button.tsx` exports `Button` and `IconButton`. `ui/Field.tsx` exports
`Field`, `TextField`, `SelectField`, `TextAreaField`, and `FormError`.
`ui/Dialog.tsx` exports `ModalDialog` with `DialogForm`, `DialogHeader`, and
`DialogFooter`; the dialog element reaches the top layer from its `open` prop,
so a wrapper never holds a ref. `ui/Section.tsx` exports `DetailSection`,
`DescriptionList`, `ScopeBadge`, and `VisuallyHidden`. Each emits the markup and
class names the pages already hand-wrote, so existing styles and acceptance
selectors keep matching while call sites migrate.

`InteractionPatterns.tsx` keeps `AsyncPanel`, `EmptyState`, `StatusBadge`,
`ActionBar`, `SectionHeader`, `DiagnosticsDetails`, and `ActionGuidance`.

`terminal/` holds the live run surface, described in
[Live runs](live-runs.md). `document/` holds the artifact review surface,
described in [Rich document review components](rich-documents.md).

## Stories

Stories live beside their component as `*.stories.tsx` in Component Story
Format, so a component and its isolated preview move together. Every pure
primitive must have one. Run the catalog:

```powershell
npm run storybook
npm run test:stories
```

`npm run storybook` serves the catalog on port 6006 with the real stylesheet and
the accessibility addon. `npm run test:stories` renders every published story in
Chromium and fails on a console error, an uncaught exception, or a story that
renders nothing. It uses `playwright.storybook.config.ts` and its own Storybook
server, so the dashboard acceptance suite in `npm run test:browser` does not pay
for a Storybook boot.

TypeScript 7 replaced the compiler API that `react-docgen-typescript` reads, so
`.storybook/main.ts` selects the Babel-based `react-docgen` for props tables.

`npm run storybook:build` writes a static catalog to `dashboard/storybook-static`,
which is ignored by Git.

## Adding a component

Put pure markup in `src/components/ui` with a colocated story covering each
distinct state a styling pass needs to see, including the busy, empty, and
rejected presentations. Keep the API call, the route change, and the revision
decision in the page wrapper. Put derivation in a `*Model.ts` beside it with a
`node --test` case, because that logic is worth testing without a browser.
