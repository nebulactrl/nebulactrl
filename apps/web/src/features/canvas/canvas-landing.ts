import type { CanvasDocument } from "#/api/models";

/**
 * Something other than the canvas saved a layout that holds new cards: a
 * template stack, whose group and positions the server wrote itself. The
 * canvas must take that layout (and its version) instead of laying the new
 * services out again and overwriting it.
 */
export interface Landing {
	/** The layout and version the server saved. */
	document: CanvasDocument;
	/** The new group's id; the canvas brings it into view. */
	groupId: string;
	/** The new services; the landing waits until the canvas lists them all. */
	serviceIds: readonly string[];
}

type Lander = (landing: Landing) => void;
let active: Lander | null = null;

/** The mounted canvas takes landings; returns the function that lets go. */
export function registerLanding(lander: Lander): () => void {
	active = lander;
	return () => {
		if (active === lander) active = null;
	};
}

/** Hands a landing to the canvas on screen; false when none is mounted. */
export function landOnCanvas(landing: Landing): boolean {
	if (!active) return false;
	active(landing);
	return true;
}

interface Fittable {
	getInternalNode(id: string): { measured: { width?: number } } | undefined;
	fitView(options: {
		nodes: { id: string }[];
		padding: number;
		maxZoom: number;
		duration: number;
	}): unknown;
}

/**
 * Fits the view to a node once the canvas has drawn and measured it: right
 * after a layout is adopted the node is still a frame away.
 */
export function fitWhenMeasured(
	flow: Fittable,
	nodeId: string,
	duration: number,
	frames = 40,
): void {
	const node = flow.getInternalNode(nodeId);
	if (node?.measured.width) {
		void flow.fitView({
			nodes: [{ id: nodeId }],
			padding: 0.25,
			maxZoom: 1,
			duration,
		});
		return;
	}
	if (frames <= 0) return;
	window.requestAnimationFrame(() =>
		fitWhenMeasured(flow, nodeId, duration, frames - 1),
	);
}
