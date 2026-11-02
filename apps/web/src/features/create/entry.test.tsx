import { afterEach, describe, expect, test } from "bun:test";
import { act, render, renderHook } from "@testing-library/react";
import {
	composeImportActions,
	useComposeImportState,
} from "#/features/compose-import/store";
import {
	coolifyImportActions,
	useCoolifyImportState,
} from "#/features/coolify-import/store";
import {
	routeCreate,
	useRouteCreateRequest,
} from "#/features/proxy/create-store";
import {
	createStackActions,
	useCreateStackState,
} from "#/features/stacks/store";
import {
	templatesActions,
	useTemplatesState,
} from "#/features/templates/store";
import { EntryMark, openCreateEntry } from "./entry";
import type { MenuEntryId } from "./lib";
import { createActions, useCreateState } from "./store";

afterEach(() => {
	act(() => {
		createActions.closeDialog();
		createActions.setMenuOpen(false);
		routeCreate.close();
		createStackActions.close();
		templatesActions.close();
		composeImportActions.close();
		coolifyImportActions.close();
	});
});

/** Every store an entry can open a flow in, read the way the hosts read them. */
function watchFlows() {
	return {
		create: renderHook(() => useCreateState()).result,
		route: renderHook(() => useRouteCreateRequest()).result,
		stack: renderHook(() => useCreateStackState()).result,
		templates: renderHook(() => useTemplatesState()).result,
		compose: renderHook(() => useComposeImportState()).result,
		coolify: renderHook(() => useCoolifyImportState()).result,
	};
}

describe("openCreateEntry", () => {
	test.each(["git", "image", "database"] as const)(
		"%s opens the service dialog of that kind with the drop position and engine",
		(id) => {
			const flows = watchFlows();
			act(() =>
				openCreateEntry(id, { position: { x: 4, y: 9 }, engine: "postgres" }),
			);
			expect(flows.create.current.request).toEqual({
				kind: id,
				position: { x: 4, y: 9 },
				engine: "postgres",
			});
			expect(flows.route.current).toBeNull();
			expect(flows.stack.current.open).toBe(false);
		},
	);

	test.each(["external", "port"] as const)(
		"%s closes the menu and opens the route dialog in that mode",
		(id) => {
			const flows = watchFlows();
			act(() => createActions.setMenuOpen(true));
			act(() => openCreateEntry(id));
			expect(flows.create.current.menu).toBeNull();
			expect(flows.create.current.request).toBeNull();
			expect(flows.route.current).toEqual({ mode: id });
		},
	);

	test("a route entry carries the drop position and leaves it out when there is none", () => {
		const flows = watchFlows();
		act(() => openCreateEntry("external", { position: { x: 1, y: 2 } }));
		expect(flows.route.current).toEqual({
			mode: "external",
			position: { x: 1, y: 2 },
		});
		act(() => openCreateEntry("port"));
		expect(flows.route.current).toEqual({ mode: "port" });
		expect(flows.route.current).not.toHaveProperty("position");
	});

	test("stack closes the menu and opens the stack dialog where it was dropped", () => {
		const flows = watchFlows();
		act(() => createActions.setMenuOpen(true));
		act(() => openCreateEntry("stack", { position: { x: 30, y: 40 } }));
		expect(flows.create.current.menu).toBeNull();
		expect(flows.stack.current).toEqual({
			open: true,
			position: { x: 30, y: 40 },
		});
	});

	test("templates closes the menu and opens the gallery on its first page", () => {
		const flows = watchFlows();
		act(() => createActions.setMenuOpen(true));
		act(() => openCreateEntry("templates"));
		expect(flows.create.current.menu).toBeNull();
		expect(flows.templates.current.open).toBe(true);
		expect(flows.templates.current.templateId).toBeNull();
	});

	test("compose closes the menu and opens the Compose import", () => {
		const flows = watchFlows();
		act(() => createActions.setMenuOpen(true));
		act(() => openCreateEntry("compose"));
		expect(flows.create.current.menu).toBeNull();
		expect(flows.compose.current.open).toBe(true);
		expect(flows.coolify.current.open).toBe(false);
	});

	test("coolify closes the menu and opens the Coolify import", () => {
		const flows = watchFlows();
		act(() => createActions.setMenuOpen(true));
		act(() => openCreateEntry("coolify"));
		expect(flows.create.current.menu).toBeNull();
		expect(flows.coolify.current.open).toBe(true);
		expect(flows.compose.current.open).toBe(false);
	});
});

describe("EntryMark", () => {
	/** What each entry's tile is drawn from: a brand sprite symbol or a lucide glyph. */
	const MARKS: Array<[MenuEntryId, string]> = [
		["git", 'use[href$="#brand-github"]'],
		["image", 'use[href$="#brand-docker"]'],
		["database", "svg.lucide-database"],
		["external", "svg.lucide-globe"],
		["port", "svg.lucide-network"],
		["stack", "svg.lucide-boxes"],
		["templates", "svg.lucide-layers"],
		["compose", "svg.lucide-file-code"],
		["coolify", "svg.lucide-arrow-right-left"],
	];

	test.each(MARKS)("%s draws its own glyph", (id, selector) => {
		const { container } = render(<EntryMark id={id} size={22} />);
		expect(container.querySelector(selector)).not.toBeNull();
		// Exactly one mark, and none of the other entries' glyphs.
		for (const [other, otherSelector] of MARKS) {
			if (other === id) continue;
			expect(container.querySelector(otherSelector)).toBeNull();
		}
	});

	test("an engine draws that database engine's own brand, whatever the entry id", () => {
		const { container } = render(
			<EntryMark id="database" engine="postgres" size={22} />,
		);
		expect(
			container.querySelector('use[href$="#brand-postgresql"]'),
		).not.toBeNull();
		expect(container.querySelector("svg.lucide-database")).toBeNull();
	});

	test("an engine without a brand (Valkey) falls back to its key glyph", () => {
		const { container } = render(
			<EntryMark id="database" engine="valkey" size={22} />,
		);
		expect(container.querySelector("svg.lucide-key-round")).not.toBeNull();
	});

	test("the tile has the requested size", () => {
		const { container } = render(<EntryMark id="templates" size={40} />);
		const tile = container.querySelector<HTMLElement>(
			'[data-slot="service-icon"]',
		);
		expect(tile?.style.width).toBe("40px");
		expect(tile?.style.height).toBe("40px");
	});
});
