import type { LinkFlowEdge } from "../service-edge";
import type { AppNode } from "./flow";

/** `a` and `b` are the same value, or the same flat object or array of the same values. */
function sameFlat(a: unknown, b: unknown): boolean {
	if (a === b) return true;
	if (typeof a !== "object" || typeof b !== "object" || !a || !b) return false;
	if (Array.isArray(a) !== Array.isArray(b)) return false;
	const keysA = Object.keys(a);
	if (keysA.length !== Object.keys(b).length) return false;
	return keysA.every(
		(key) =>
			(a as Record<string, unknown>)[key] ===
			(b as Record<string, unknown>)[key],
	);
}

/**
 * Whether `next` (a node or edge just built from the services and the
 * layout) draws exactly what `previous` (the one ReactFlow already holds)
 * draws. Only the keys `next` sets are compared: the rest (`measured`,
 * `dragging`...) are ReactFlow's own bookkeeping.
 *
 * `data` goes one level deeper than the other keys, because the builders make
 * a fresh `plugs`, `members` or `references` each time that usually holds the
 * same values.
 */
export function sameElement(previous: object, next: object): boolean {
	const a = previous as Record<string, unknown>;
	const b = next as Record<string, unknown>;
	return Object.keys(b).every((key) => {
		if (key !== "data") return sameFlat(a[key], b[key]);
		const dataA = a.data as Record<string, unknown>;
		const dataB = b.data as Record<string, unknown>;
		return Object.keys(dataB).every((field) =>
			sameFlat(dataA[field], dataB[field]),
		);
	});
}

/**
 * `next` built on top of what the canvas holds: an element that is unchanged
 * keeps its identity, so ReactFlow (and every card and cable) skips it, and
 * only what really moved or changed is rendered again. When nothing did,
 * `previous` itself comes back and React skips the render.
 */
function reuse<T extends { id: string }>(previous: T[], next: T[]): T[] {
	const before = new Map(previous.map((element) => [element.id, element]));
	const merged = next.map((element) => {
		const old = before.get(element.id);
		return old && sameElement(old, element) ? old : element;
	});
	const unchanged =
		merged.length === previous.length &&
		merged.every((element, index) => element === previous[index]);
	return unchanged ? previous : merged;
}

export const reuseNodes = reuse<AppNode>;
export const reuseEdges = reuse<LinkFlowEdge>;
