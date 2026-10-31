import { describe, expect, test } from "bun:test";
import { armSources, pairTargets } from "./shared-elements";
import { NameRegistry, type StyleLike } from "./vt-names";

class FakeStyle implements StyleLike {
	readonly values = new Map<string, string>();
	getPropertyValue(property: string): string {
		return this.values.get(property) ?? "";
	}
	setProperty(property: string, value: string): void {
		this.values.set(property, value);
	}
	removeProperty(property: string): string {
		const previous = this.getPropertyValue(property);
		this.values.delete(property);
		return previous;
	}
}

interface El {
	id: string;
	style: FakeStyle;
}

const el = (id: string): El => ({ id, style: new FakeStyle() });
const nameOf = (element: El) =>
	element.style.getPropertyValue("view-transition-name");

describe("armSources", () => {
	test("names each source and returns what to look for later", () => {
		const registry = new NameRegistry<El>();
		const card = el("card");
		const armed = armSources(registry, 1, [
			{ key: "project:7", element: card },
		]);
		expect(armed).toEqual([
			{ key: "project:7", name: "vt-project_7", className: "vt-card" },
		]);
		expect(nameOf(card)).toBe("vt-project_7");
	});

	test("a key that is not kind:id is skipped and names nothing", () => {
		const registry = new NameRegistry<El>();
		const card = el("card");
		expect(armSources(registry, 1, [{ key: "oops", element: card }])).toEqual(
			[],
		);
		expect(nameOf(card)).toBe("");
	});
});

describe("pairTargets", () => {
	test("the destination takes the name and the departed source loses it", () => {
		const registry = new NameRegistry<El>();
		const label = el("sidebar-label");
		const title = el("page-title");
		const armed = armSources(registry, 1, [
			{ key: "nav:servers", element: label },
		]);
		const paired = pairTargets(registry, 1, armed, () => [label, title]);
		expect(paired).toBe(1);
		expect(nameOf(label)).toBe("");
		expect(nameOf(title)).toBe("vt-nav_servers");
		expect(title.style.getPropertyValue("view-transition-class")).toBe(
			"vt-title",
		);
	});

	test("with no counterpart the source keeps its name", () => {
		const registry = new NameRegistry<El>();
		const label = el("label");
		const armed = armSources(registry, 1, [
			{ key: "nav:canvas", element: label },
		]);
		expect(pairTargets(registry, 1, armed, () => [label])).toBe(0);
		expect(pairTargets(registry, 1, armed, () => [])).toBe(0);
		expect(nameOf(label)).toBe("vt-nav_canvas");
	});

	test("a card that unmounted hands its name to the page it opened", () => {
		const registry = new NameRegistry<El>();
		const card = el("card");
		const canvas = el("canvas-root");
		const armed = armSources(registry, 1, [
			{ key: "project:7", element: card },
		]);
		expect(pairTargets(registry, 1, armed, () => [canvas])).toBe(1);
		expect(registry.holder("vt-project_7")).toBe(canvas);
		registry.release(1);
		expect(nameOf(canvas)).toBe("");
		expect(nameOf(card)).toBe("");
		expect(registry.size).toBe(0);
	});

	test("only the first element that is not the source is named", () => {
		const registry = new NameRegistry<El>();
		const card = el("card");
		const first = el("first");
		const second = el("second");
		const armed = armSources(registry, 1, [{ key: "server:1", element: card }]);
		pairTargets(registry, 1, armed, () => [card, first, second]);
		expect(nameOf(first)).toBe("vt-server_1");
		expect(nameOf(second)).toBe("");
	});
});
