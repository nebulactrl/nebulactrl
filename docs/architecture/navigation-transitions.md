---
title: Navigation transitions
description: "How route changes animate in the web app: view transitions, shared elements, reduced motion."
section: Internals
order: 20
---

# Navigation transitions

Every route change in the web app runs as a [View Transition](https://developer.mozilla.org/docs/Web/API/View_Transition_API). Cards, rows, sidebar labels and page titles morph into their destination; the rest of the page cross-fades with a small lift.

Code lives in `apps/web/src/lib/transitions/`; the look lives in the `::view-transition-*` block at the end of `apps/web/src/styles.css`.

## What plays

| Variant | When | Timing |
|---|---|---|
| `page` | A card, row or link press with the pointer; back and forward; navigations started from code | Content area 180 ms cross-fade plus an 8 px lift; shared elements 260 ms, ease-out |
| `fast` | A press inside the sidebar or the command palette; any keyboard activation | 150 ms throughout |
| `reduced` | `prefers-reduced-motion: reduce` | Plain 120 ms cross-fade, no lift, nothing morphs |
| none | Browser without `document.startViewTransition`; a change of search or hash only; a held key or an arrow, Home, End or Page key; a navigation within 120 ms of the previous one; the initial load | Instant |

Only the content area (`#main-content`) animates. The sidebar and the top bar are in the unchanged root snapshot and show their new state at once. A navigation with no shell on either side (login, setup) cross-fades the whole document instead.

## How names are assigned

An element opts in with `data-vt="<kind>:<id>"` (helpers `vt(kind, id)` and `vtAnchor(kind, id)` in `#/lib/transitions`). That attribute has no visual effect. The `view-transition-name` itself exists only for the length of one transition:

1. The router calls `router.startViewTransition(update)` for every committed navigation. `installRouteTransitions` (called in `main.tsx`) replaces that member, because neither `defaultViewTransition` nor the per-navigation `viewTransition` option hands back the `ViewTransition` object that tells us when to clean up.
2. `interaction.ts` has recorded the last pointer or key press. If it landed on (or inside a link or row containing) a `data-vt` element, that element is the source. Otherwise the leaving page's `data-vt-anchor` elements are the sources (a canvas shrinks back into its card).
3. The sources and `#main-content` get their names, and the browser captures the old page.
4. After `update()` has rendered the new page, the first element carrying the same `data-vt` key that is not the source takes the name. A persistent source (a sidebar label that becomes a page title) hands it over. With no counterpart the source keeps its name and cross-fades in place.
5. When the transition finishes or is skipped, every name that transition set is removed and the inline style it replaced is restored. A transition that was superseded only releases what it still holds.

Because only the pressed element is named, duplicates (many rows for one service) never collide.

## Where it is wired

| Source | Target |
|---|---|
| Project card (`project:<id>`) | Canvas page root (`project:<id>`, anchor): grows in, and shrinks back on the way out |
| Server card (`server:<id>`) | Server page header (`server:<id>`, anchor), both ways |
| Deployment and service table rows (`service:<id>`) | Inspector header (`service:<id>`) |
| Sidebar label (`nav:<page>`) | Page title (`nav:<page>`) in `PageFrame` |

To add one: put `{...vt("kind", id)}` on the source and the same on its destination, and add the kind to `CLASS_BY_KIND` in `vt-names.ts` if it needs its own class.

## Tests

`bun test src/lib/transitions` covers the name registry (assignment, hand-over, restore, superseded owners), the navigation classifier (variants, reduced motion, repeating keys, unsupported browsers, rapid navigations) and arming/pairing of shared elements. The DOM glue (`interaction.ts`, `route-transitions.ts`) is not unit-tested.
