// Pure pieces of the shared-element system: how a `data-vt` key becomes a
// `view-transition-name`, and the registry that assigns a name to one
// element at a time and takes it away again. Nothing here touches the
// document, so it runs under `bun test` with plain objects standing in for
// elements.
//
// Names are never left on the page. A source element (a card, a row, a
// sidebar label) only carries `data-vt="<kind>:<id>"`, which has no visual
// effect. The names themselves are put on the elements involved in one
// navigation just before it starts and taken off when it finishes, so two
// cards can never share a name and idle pages carry no snapshot cost.

/** The kinds of element that morph, and the CSS class each one gets. */
const CLASS_BY_KIND = {
	project: "vt-card",
	server: "vt-card",
	service: "vt-row",
	nav: "vt-title",
	tab: "vt-tab",
} as const;

export type VtKind = keyof typeof CLASS_BY_KIND;

/** The name of the page area (`#main-content`) that crossfades and lifts. */
export const PAGE_NAME = "vt-page";
export const PAGE_CLASS = "vt-page";

/** The name of the body an in-page tab switch crossfades. */
export const TAB_BODY_NAME = "vt-tab-body";
export const TAB_BODY_CLASS = "vt-tab-body";

const NAME_PROPERTY = "view-transition-name";
const CLASS_PROPERTY = "view-transition-class";

/** The `data-vt` value for an element: `kind:id`. */
export function vtKey(kind: VtKind, id: string): string {
	return `${kind}:${id}`;
}

/** Spread onto a JSX element so it can be the source or target of a morph. */
export function vt(kind: VtKind, id: string): { "data-vt": string } {
	return { "data-vt": vtKey(kind, id) };
}

/**
 * Like `vt`, and the element is also armed on its own when a navigation
 * leaves its page without having been started from a keyed element: a
 * canvas that shrinks back into its project card, a server's page header
 * that returns to its card.
 */
export function vtAnchor(
	kind: VtKind,
	id: string,
): { "data-vt": string; "data-vt-anchor": "" } {
	return { "data-vt": vtKey(kind, id), "data-vt-anchor": "" };
}

/**
 * The valid CSS custom-ident for a key, or null when the key is not of the
 * `kind:id` shape. The `vt-` prefix keeps every name clear of the reserved
 * words (`none`, `auto`) and of the fixed names above, none of which
 * contain a second segment.
 */
export function toViewTransitionName(key: string): string | null {
	const colon = key.indexOf(":");
	if (colon < 1 || colon === key.length - 1) return null;
	return `vt-${key.replace(/[^A-Za-z0-9_-]/g, "_")}`;
}

/** The `view-transition-class` for a key: its kind's class, or a plain one. */
export function classForKey(key: string): string {
	const colon = key.indexOf(":");
	const kind = colon < 0 ? "" : key.slice(0, colon);
	return Object.hasOwn(CLASS_BY_KIND, kind)
		? CLASS_BY_KIND[kind as VtKind]
		: "vt-item";
}

/** The attribute selector matching elements that carry `key`. */
export function keySelector(key: string): string {
	return `[data-vt="${key.replace(/["\\]/g, "\\$&")}"]`;
}

/** The slice of a CSS style declaration the registry writes through. */
export interface StyleLike {
	getPropertyValue(property: string): string;
	setProperty(property: string, value: string): void;
	removeProperty(property: string): string;
}

export interface NamedElement {
	style: StyleLike;
}

interface Held {
	owner: number;
	name: string;
	/** The inline values the element had before it was named, restored later. */
	previousName: string;
	previousClass: string;
}

function restoreProperty(style: StyleLike, property: string, value: string) {
	if (value === "") style.removeProperty(property);
	else style.setProperty(property, value);
}

/**
 * Who holds which `view-transition-name` right now. A name has at most one
 * holder (a duplicate would abort the transition), an element holds at most
 * one name, and everything is released by owner: a navigation that was
 * superseded finishes late, and must only take back what it still holds.
 */
export class NameRegistry<E extends NamedElement> {
	readonly #held = new Map<E, Held>();
	readonly #holders = new Map<string, E>();

	/**
	 * Gives `element` the name. Another holder of the same name loses it (its
	 * own inline values come back), which is how a persistent source such as
	 * a sidebar label hands its name to the page title that replaces it.
	 */
	assign(element: E, name: string, className: string, owner: number): void {
		const current = this.#holders.get(name);
		if (current !== undefined && current !== element) this.#restore(current);

		const existing = this.#held.get(element);
		if (existing) {
			if (existing.name !== name) this.#holders.delete(existing.name);
			existing.name = name;
			existing.owner = owner;
		} else {
			this.#held.set(element, {
				owner,
				name,
				previousName: element.style.getPropertyValue(NAME_PROPERTY),
				previousClass: element.style.getPropertyValue(CLASS_PROPERTY),
			});
		}
		this.#holders.set(name, element);
		element.style.setProperty(NAME_PROPERTY, name);
		if (className !== "") element.style.setProperty(CLASS_PROPERTY, className);
		else element.style.removeProperty(CLASS_PROPERTY);
	}

	/** The element currently holding `name`. */
	holder(name: string): E | undefined {
		return this.#holders.get(name);
	}

	/** Takes back every name `owner` still holds. */
	release(owner: number): void {
		for (const [element, held] of [...this.#held]) {
			if (held.owner === owner) this.#restore(element);
		}
	}

	/** How many elements are named at the moment. */
	get size(): number {
		return this.#held.size;
	}

	#restore(element: E) {
		const held = this.#held.get(element);
		if (!held) return;
		restoreProperty(element.style, NAME_PROPERTY, held.previousName);
		restoreProperty(element.style, CLASS_PROPERTY, held.previousClass);
		this.#held.delete(element);
		if (this.#holders.get(held.name) === element) {
			this.#holders.delete(held.name);
		}
	}
}
