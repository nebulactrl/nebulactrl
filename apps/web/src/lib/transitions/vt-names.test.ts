import { describe, expect, test } from "bun:test";
import {
	classForKey,
	keySelector,
	NameRegistry,
	type StyleLike,
	toViewTransitionName,
	vt,
	vtAnchor,
	vtKey,
} from "./vt-names";

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

interface FakeElement {
	style: FakeStyle;
}

function element(initial: Record<string, string> = {}): FakeElement {
	const style = new FakeStyle();
	for (const [property, value] of Object.entries(initial)) {
		style.setProperty(property, value);
	}
	return { style };
}

const NAME = "view-transition-name";
const CLASS = "view-transition-class";

describe("keys and names", () => {
	test("a key is kind:id and spreads as data-vt", () => {
		expect(vtKey("project", "abc")).toBe("project:abc");
		expect(vt("server", "s1")).toEqual({ "data-vt": "server:s1" });
		expect(vtAnchor("project", "p1")).toEqual({
			"data-vt": "project:p1",
			"data-vt-anchor": "",
		});
	});

	test("names are valid idents with the vt- prefix", () => {
		expect(toViewTransitionName("project:0b8e-4C")).toBe("vt-project_0b8e-4C");
		expect(toViewTransitionName("nav:api tokens/x")).toBe(
			"vt-nav_api_tokens_x",
		);
	});

	test("a key without kind or id is not a name", () => {
		expect(toViewTransitionName("project")).toBeNull();
		expect(toViewTransitionName(":abc")).toBeNull();
		expect(toViewTransitionName("project:")).toBeNull();
		expect(toViewTransitionName("")).toBeNull();
	});

	test("two different keys never produce the same name", () => {
		expect(toViewTransitionName("project:a")).not.toBe(
			toViewTransitionName("server:a"),
		);
	});

	test("the class follows the kind", () => {
		expect(classForKey("project:1")).toBe("vt-card");
		expect(classForKey("server:1")).toBe("vt-card");
		expect(classForKey("service:1")).toBe("vt-row");
		expect(classForKey("nav:servers")).toBe("vt-title");
		expect(classForKey("tab:x")).toBe("vt-tab");
		expect(classForKey("toString:1")).toBe("vt-item");
		expect(classForKey("nokind")).toBe("vt-item");
	});

	test("the selector escapes quotes and backslashes", () => {
		expect(keySelector("project:a")).toBe('[data-vt="project:a"]');
		expect(keySelector('x:"y\\z')).toBe('[data-vt="x:\\"y\\\\z"]');
	});
});

describe("NameRegistry", () => {
	test("assigns a name and class, and release restores a clean element", () => {
		const registry = new NameRegistry<FakeElement>();
		const card = element();
		registry.assign(card, "vt-a", "vt-card", 1);
		expect(card.style.getPropertyValue(NAME)).toBe("vt-a");
		expect(card.style.getPropertyValue(CLASS)).toBe("vt-card");
		expect(registry.holder("vt-a")).toBe(card);

		registry.release(1);
		expect(card.style.getPropertyValue(NAME)).toBe("");
		expect(card.style.getPropertyValue(CLASS)).toBe("");
		expect(registry.holder("vt-a")).toBeUndefined();
		expect(registry.size).toBe(0);
	});

	test("release restores inline values the element already had", () => {
		const registry = new NameRegistry<FakeElement>();
		const card = element({ [NAME]: "mine", [CLASS]: "mine-class" });
		registry.assign(card, "vt-a", "vt-card", 1);
		registry.release(1);
		expect(card.style.getPropertyValue(NAME)).toBe("mine");
		expect(card.style.getPropertyValue(CLASS)).toBe("mine-class");
	});

	test("a name has one holder: the earlier one gets its own values back", () => {
		const registry = new NameRegistry<FakeElement>();
		const source = element();
		const target = element();
		registry.assign(source, "vt-a", "vt-title", 1);
		registry.assign(target, "vt-a", "vt-title", 1);
		expect(source.style.getPropertyValue(NAME)).toBe("");
		expect(target.style.getPropertyValue(NAME)).toBe("vt-a");
		expect(registry.holder("vt-a")).toBe(target);
		expect(registry.size).toBe(1);
	});

	test("an element holds one name: naming it again moves it", () => {
		const registry = new NameRegistry<FakeElement>();
		const card = element();
		registry.assign(card, "vt-a", "vt-card", 1);
		registry.assign(card, "vt-b", "vt-row", 1);
		expect(registry.holder("vt-a")).toBeUndefined();
		expect(registry.holder("vt-b")).toBe(card);
		expect(card.style.getPropertyValue(CLASS)).toBe("vt-row");
		registry.release(1);
		expect(card.style.getPropertyValue(NAME)).toBe("");
	});

	test("a late release by a superseded owner leaves the newer owner's names", () => {
		const registry = new NameRegistry<FakeElement>();
		const main = element();
		const oldCard = element();
		const newCard = element();
		registry.assign(main, "vt-page", "vt-page", 1);
		registry.assign(oldCard, "vt-a", "vt-card", 1);
		// A second navigation starts before the first one has finished.
		registry.assign(main, "vt-page", "vt-page", 2);
		registry.assign(newCard, "vt-b", "vt-card", 2);

		registry.release(1);
		expect(main.style.getPropertyValue(NAME)).toBe("vt-page");
		expect(newCard.style.getPropertyValue(NAME)).toBe("vt-b");
		expect(oldCard.style.getPropertyValue(NAME)).toBe("");

		registry.release(2);
		expect(main.style.getPropertyValue(NAME)).toBe("");
		expect(newCard.style.getPropertyValue(NAME)).toBe("");
		expect(registry.size).toBe(0);
	});

	test("an element named by two owners keeps its original inline values", () => {
		const registry = new NameRegistry<FakeElement>();
		const main = element({ [NAME]: "orig" });
		registry.assign(main, "vt-page", "vt-page", 1);
		registry.assign(main, "vt-page", "vt-page", 2);
		registry.release(2);
		expect(main.style.getPropertyValue(NAME)).toBe("orig");
	});

	test("releasing an owner with nothing held is a no-op", () => {
		const registry = new NameRegistry<FakeElement>();
		registry.release(99);
		expect(registry.size).toBe(0);
	});
});
