import type {
	CanvasGroup,
	CanvasLayout,
	Point,
	ServiceLink,
} from "#/api/models";
import { ceilGrid, type LayerVertex, layeredLayout, NODE_GAP } from "./layered";

/** The service card's width and a measured-enough height (a card with a volume strip is a little taller). */
export const NODE_WIDTH = 288;
export const NODE_HEIGHT = 176;
/** A collapsed group is one summary card of this size. */
export const GROUP_SUMMARY_HEIGHT = 72;
/** A group frame's padding around its members, with room for its header. */
export const GROUP_PAD = 24;
export const GROUP_HEADER = 40;

const GAP_X = 80;
const GAP_Y = 48;
const FRESH_COLUMNS = 3;
const STACK_ROWS = 4;

export function emptyLayout(): CanvasLayout {
	return { nodes: {}, groups: [], viewport: null };
}

interface Rect {
	x: number;
	y: number;
	w: number;
	h: number;
}

function overlaps(a: Rect, b: Rect, margin = 0): boolean {
	return (
		a.x < b.x + b.w + margin &&
		a.x + a.w + margin > b.x &&
		a.y < b.y + b.h + margin &&
		a.y + a.h + margin > b.y
	);
}

/** The width and height a service's node takes on the canvas. */
export type SizeOf = (serviceId: string) => Size;

/** Every ordinary service card: the fixed card size. */
export const defaultSize: SizeOf = () => ({ w: NODE_WIDTH, h: NODE_HEIGHT });

function nodeRect(p: Point, size: Size): Rect {
	return { x: p.x, y: p.y, w: size.w, h: size.h };
}

/** Whether a group draws as its single summary card. */
export function groupRect(group: CanvasGroup): Rect {
	return { x: group.x, y: group.y, w: group.w, h: group.h };
}

export interface MergeResult {
	layout: CanvasLayout;
	/** Services that had no saved position and were placed now. */
	placed: string[];
	/** Whether pruning removed anything (deleted services, empty groups). */
	pruned: boolean;
}

/**
 * Reconciles a saved layout with the services that exist now. Saved
 * positions are kept as they are; positions and group members of services
 * that no longer exist are dropped; a service with no position is placed to
 * the right of everything already on the canvas (or on a fresh grid when the
 * canvas is empty), never over another card or a group frame.
 */
export function mergeLayout(
	serviceIds: readonly string[],
	saved: CanvasLayout | null | undefined,
	sizeOf: SizeOf = defaultSize,
): MergeResult {
	const known = new Set(serviceIds);
	const base = saved ?? emptyLayout();

	const nodes: Record<string, Point> = {};
	for (const [id, point] of Object.entries(base.nodes)) {
		if (known.has(id)) nodes[id] = point;
	}
	let pruned = Object.keys(nodes).length !== Object.keys(base.nodes).length;

	const groups: CanvasGroup[] = [];
	for (const group of base.groups) {
		const members = group.serviceIds.filter((id) => known.has(id));
		if (members.length !== group.serviceIds.length) pruned = true;
		if (members.length === 0) {
			pruned = true;
			continue;
		}
		groups.push({ ...group, serviceIds: members });
	}

	const missing = serviceIds.filter((id) => nodes[id] === undefined);
	const placed: string[] = [];
	if (missing.length > 0) {
		const occupied: Rect[] = [
			...Object.entries(nodes).map(([id, p]) => nodeRect(p, sizeOf(id))),
			...groups.map(groupRect),
		];
		const anchor = occupied.length === 0 ? null : bounds(occupied);
		let index = 0;
		for (const id of missing) {
			let x: number;
			let y: number;
			if (anchor === null) {
				x = (index % FRESH_COLUMNS) * (NODE_WIDTH + GAP_X);
				y = Math.floor(index / FRESH_COLUMNS) * (NODE_HEIGHT + GAP_Y);
			} else {
				const column = Math.floor(index / STACK_ROWS);
				x = anchor.x + anchor.w + GAP_X + column * (NODE_WIDTH + GAP_X);
				y = anchor.y + (index % STACK_ROWS) * (NODE_HEIGHT + GAP_Y);
			}
			let candidate = { x, y };
			// Bump down until the slot is free of every card and frame.
			const size = sizeOf(id);
			while (occupied.some((r) => overlaps(nodeRect(candidate, size), r, 8))) {
				candidate = { x: candidate.x, y: candidate.y + size.h + GAP_Y };
			}
			nodes[id] = candidate;
			occupied.push(nodeRect(candidate, size));
			placed.push(id);
			index += 1;
		}
	}

	return {
		layout: { nodes, groups, viewport: base.viewport ?? null },
		placed,
		pruned,
	};
}

function bounds(rects: readonly Rect[]): Rect {
	let minX = Number.POSITIVE_INFINITY;
	let minY = Number.POSITIVE_INFINITY;
	let maxX = Number.NEGATIVE_INFINITY;
	let maxY = Number.NEGATIVE_INFINITY;
	for (const r of rects) {
		minX = Math.min(minX, r.x);
		minY = Math.min(minY, r.y);
		maxX = Math.max(maxX, r.x + r.w);
		maxY = Math.max(maxY, r.y + r.h);
	}
	return { x: minX, y: minY, w: maxX - minX, h: maxY - minY };
}

/** A group's frame around its members' current positions. */
export function fitGroup(
	group: CanvasGroup,
	nodes: Readonly<Record<string, Point>>,
	sizeOf: SizeOf = defaultSize,
): CanvasGroup {
	const rects = group.serviceIds.flatMap((id) => {
		const p = nodes[id];
		return p ? [nodeRect(p, sizeOf(id))] : [];
	});
	if (rects.length === 0) return group;
	const b = bounds(rects);
	return {
		...group,
		x: b.x - GROUP_PAD,
		y: b.y - GROUP_PAD - GROUP_HEADER,
		w: b.w + GROUP_PAD * 2,
		h: b.h + GROUP_PAD * 2 + GROUP_HEADER,
	};
}

/** Re-fits every expanded group's frame to its members; collapsed ones keep their place. */
export function fitGroups(
	layout: CanvasLayout,
	sizeOf: SizeOf = defaultSize,
): CanvasLayout {
	return {
		...layout,
		groups: layout.groups.map((g) =>
			g.collapsed ? g : fitGroup(g, layout.nodes, sizeOf),
		),
	};
}

/** Groups the given services, taking them out of any group they were in. */
export function createGroup(
	layout: CanvasLayout,
	serviceIds: readonly string[],
	id: string,
	name: string,
	sizeOf: SizeOf = defaultSize,
): CanvasLayout {
	const taken = new Set(serviceIds);
	const groups = layout.groups
		.map((g) => ({
			...g,
			serviceIds: g.serviceIds.filter((s) => !taken.has(s)),
		}))
		.filter((g) => g.serviceIds.length > 0);
	const fresh = fitGroup(
		{
			id,
			name,
			x: 0,
			y: 0,
			w: NODE_WIDTH,
			h: NODE_HEIGHT,
			collapsed: false,
			serviceIds: [...serviceIds],
		},
		layout.nodes,
		sizeOf,
	);
	return fitGroups({ ...layout, groups: [...groups, fresh] }, sizeOf);
}

export function removeGroup(layout: CanvasLayout, id: string): CanvasLayout {
	return { ...layout, groups: layout.groups.filter((g) => g.id !== id) };
}

export function renameGroup(
	layout: CanvasLayout,
	id: string,
	name: string,
): CanvasLayout {
	return {
		...layout,
		groups: layout.groups.map((g) => (g.id === id ? { ...g, name } : g)),
	};
}

export function toggleGroup(
	layout: CanvasLayout,
	id: string,
	sizeOf: SizeOf = defaultSize,
): CanvasLayout {
	return fitGroups(
		{
			...layout,
			groups: layout.groups.map((g) =>
				g.id === id ? { ...g, collapsed: !g.collapsed } : g,
			),
		},
		sizeOf,
	);
}

/** The group a service belongs to, if any. */
export function groupOf(
	layout: CanvasLayout,
	serviceId: string,
): CanvasGroup | undefined {
	return layout.groups.find((g) => g.serviceIds.includes(serviceId));
}

export interface Size {
	w: number;
	h: number;
}

type Link = Pick<ServiceLink, "fromServiceId" | "toServiceId">;

/** What Arrange needs to know about the services besides their links. */
export interface LayoutTraits {
	/** Every service in list order: the tie-break of every choice, so one graph gives one layout. */
	order: readonly string[];
	/** The services the Internet reaches, in domain order. */
	entryIds: readonly string[];
	/** Databases and caches: they sit in the last column. */
	dataIds: ReadonlySet<string>;
}

export const NO_TRAITS: LayoutTraits = {
	order: [],
	entryIds: [],
	dataIds: new Set(),
};

/** Services in list order, unknown ones after them by id: the one tie-break of every choice. */
function listOrder(traits: LayoutTraits): (a: string, b: string) => number {
	const order = new Map(traits.order.map((id, i) => [id, i] as const));
	return (a, b) =>
		(order.get(a) ?? Number.POSITIVE_INFINITY) -
			(order.get(b) ?? Number.POSITIVE_INFINITY) ||
		(a < b ? -1 : a > b ? 1 : 0);
}

/** Free space between two clusters side by side. */
export const CLUSTER_GAP = 120;
/** Free space between the loose cards of the last cluster. */
const LOOSE_GAP = 72;

interface Vertex {
	/** The service id, or `group:<id>`. */
	key: string;
	group?: CanvasGroup;
	size: Size;
	/** From the frame's corner to the first member's: the lattice aligns members, not frames. */
	insetY: number;
	entry: boolean;
	/** Members' corners from the frame's content origin (an expanded group). */
	inner?: Map<string, Point>;
}

/**
 * Layered left-to-right layout of the whole canvas. Each group is laid out
 * inside first (an expanded group arranges its own members; a collapsed one
 * keeps them as they are) and then counts as one box. Boxes that link to each
 * other form a cluster with its own columns (consumers left, the data they
 * use right); clusters stand side by side with `CLUSTER_GAP` between them,
 * the ones the Internet reaches first, in domain order; services with no link
 * at all share a grid at the end. Group frames come from their members after
 * layout, so a frame holds its members and nothing else, and the Internet
 * node (placed by `internetPosition`) ends up above all of it.
 */
export function autoLayout(
	layout: CanvasLayout,
	links: readonly Link[],
	sizeOf: SizeOf = defaultSize,
	traits: LayoutTraits = NO_TRAITS,
): CanvasLayout {
	const entryRank = new Map(traits.entryIds.map((id, i) => [id, i] as const));
	const canonical = listOrder(traits);

	const ids = Object.keys(layout.nodes).sort(canonical);
	const groupOfService = new Map<string, CanvasGroup>();
	for (const group of layout.groups) {
		for (const id of group.serviceIds) groupOfService.set(id, group);
	}
	const placedIds = new Set(ids);
	const dataIds = traits.dataIds;

	// One vertex per group (in the order its first member comes) and per loose service.
	const vertices: Vertex[] = [];
	const vertexOf = new Map<string, number>();
	for (const id of ids) {
		const group = groupOfService.get(id);
		const key = group ? `group:${group.id}` : id;
		if (vertexOf.has(key)) continue;
		vertexOf.set(key, vertices.length);
		if (!group) {
			vertices.push({
				key,
				size: sizeOf(id),
				insetY: 0,
				entry: entryRank.has(id),
			});
			continue;
		}
		const members = group.serviceIds.filter((m) => placedIds.has(m));
		members.sort(canonical);
		const entry = members.some((m) => entryRank.has(m));
		if (group.collapsed) {
			const fitted = fitGroup(group, layout.nodes, sizeOf);
			vertices.push({
				key,
				group,
				size: { w: fitted.w, h: fitted.h },
				insetY: 0,
				entry,
			});
			continue;
		}
		const inside = new Set(members);
		const result = layeredLayout(
			members.map(
				(m): LayerVertex => ({
					id: m,
					w: sizeOf(m).w,
					h: sizeOf(m).h,
					entry: entryRank.has(m),
					data: dataIds.has(m),
				}),
			),
			links
				.filter((l) => inside.has(l.fromServiceId) && inside.has(l.toServiceId))
				.map((l) => [l.fromServiceId, l.toServiceId] as const),
		);
		vertices.push({
			key,
			group,
			size: {
				w: result.width + GROUP_PAD * 2,
				h: result.height + GROUP_PAD * 2 + GROUP_HEADER,
			},
			insetY: GROUP_PAD + GROUP_HEADER,
			entry,
			inner: result.boxes,
		});
	}

	// Links between vertices; clusters are their connected components.
	const edges: Array<readonly [string, string]> = [];
	const parent = vertices.map((_, i) => i);
	const root = (i: number): number => {
		let r = i;
		while (parent[r] !== r) r = parent[r] as number;
		let walk = i;
		while (parent[walk] !== r) {
			const next = parent[walk] as number;
			parent[walk] = r;
			walk = next;
		}
		return r;
	};
	for (const link of links) {
		const from = groupOfService.get(link.fromServiceId);
		const to = groupOfService.get(link.toServiceId);
		const a = vertexOf.get(from ? `group:${from.id}` : link.fromServiceId);
		const b = vertexOf.get(to ? `group:${to.id}` : link.toServiceId);
		if (a === undefined || b === undefined || a === b) continue;
		edges.push([(vertices[a] as Vertex).key, (vertices[b] as Vertex).key]);
		parent[root(a)] = root(b);
	}
	const clusters = new Map<number, number[]>();
	vertices.forEach((_, i) => {
		const r = root(i);
		clusters.set(r, [...(clusters.get(r) ?? []), i]);
	});

	// A card on its own with nothing to say joins the grid at the end.
	const loose: number[] = [];
	const linked: number[][] = [];
	for (const members of clusters.values()) {
		const only =
			members.length === 1 ? (vertices[members[0] as number] as Vertex) : null;
		if (only && !only.group && !only.entry) loose.push(members[0] as number);
		else linked.push(members);
	}
	const entryOf = (members: readonly number[]) =>
		Math.min(
			...members.flatMap((i) => {
				const v = vertices[i] as Vertex;
				const keys = v.group
					? v.group.serviceIds.filter((m) => placedIds.has(m))
					: [v.key];
				return keys.map((k) => entryRank.get(k) ?? Number.POSITIVE_INFINITY);
			}),
		);
	// Entry clusters in domain order, then the rest in list order.
	linked.sort(
		(a, b) => entryOf(a) - entryOf(b) || (a[0] as number) - (b[0] as number),
	);

	const boxes = new Map<string, Point>();
	let cursor = 0;
	for (const members of linked) {
		const result = layeredLayout(
			members.map((i): LayerVertex => {
				const v = vertices[i] as Vertex;
				const single = v.group ? undefined : v.key;
				return {
					id: v.key,
					w: v.size.w,
					h: v.size.h,
					insetY: v.insetY,
					entry: v.entry,
					data: single !== undefined && dataIds.has(single),
				};
			}),
			edges.filter(
				([a, b]) =>
					members.some((i) => (vertices[i] as Vertex).key === a) &&
					members.some((i) => (vertices[i] as Vertex).key === b),
			),
		);
		for (const [key, at] of result.boxes) {
			boxes.set(key, { x: cursor + at.x, y: at.y });
		}
		cursor += ceilGrid(result.width) + CLUSTER_GAP;
	}
	if (loose.length > 0) {
		const columns = Math.ceil(Math.sqrt(loose.length));
		const wide: number[] = [];
		const tall: number[] = [];
		loose.forEach((i, n) => {
			const size = (vertices[i] as Vertex).size;
			wide[n % columns] = Math.max(wide[n % columns] ?? 0, size.w);
			tall[Math.floor(n / columns)] = Math.max(
				tall[Math.floor(n / columns)] ?? 0,
				size.h,
			);
		});
		loose.forEach((i, n) => {
			let x = cursor;
			for (let c = 0; c < n % columns; c++) {
				x += ceilGrid(wide[c] as number) + LOOSE_GAP;
			}
			let y = 0;
			for (let r = 0; r < Math.floor(n / columns); r++) {
				y += ceilGrid(tall[r] as number) + NODE_GAP;
			}
			boxes.set((vertices[i] as Vertex).key, { x, y });
		});
	}

	const nodes: Record<string, Point> = {};
	const frames = new Map<string, CanvasGroup>();
	for (const v of vertices) {
		const at = boxes.get(v.key);
		if (!at) continue;
		if (!v.group) {
			nodes[v.key] = at;
			continue;
		}
		frames.set(v.group.id, {
			...v.group,
			x: at.x,
			y: at.y,
			w: v.size.w,
			h: v.size.h,
		});
		if (v.inner) {
			for (const [id, offset] of v.inner) {
				nodes[id] = {
					x: at.x + GROUP_PAD + offset.x,
					y: at.y + v.insetY + offset.y,
				};
			}
			continue;
		}
		// A collapsed group keeps its members where they were inside the frame.
		const old = fitGroup(v.group, layout.nodes, sizeOf);
		for (const id of v.group.serviceIds) {
			const was = layout.nodes[id];
			if (was) nodes[id] = { x: was.x - old.x + at.x, y: was.y - old.y + at.y };
		}
	}
	return {
		...layout,
		nodes,
		groups: layout.groups.map((g) => frames.get(g.id) ?? g),
	};
}

/** Whether two layouts put every card and every frame in the same place. */
export function samePlaces(a: CanvasLayout, b: CanvasLayout): boolean {
	const ids = Object.keys(a.nodes);
	if (ids.length !== Object.keys(b.nodes).length) return false;
	if (a.groups.length !== b.groups.length) return false;
	return (
		ids.every((id) => {
			const p = a.nodes[id];
			const q = b.nodes[id];
			return p && q && p.x === q.x && p.y === q.y;
		}) &&
		a.groups.every((g, i) => {
			const h = b.groups[i];
			return (
				h &&
				g.id === h.id &&
				g.x === h.x &&
				g.y === h.y &&
				g.w === h.w &&
				g.h === h.h
			);
		})
	);
}

/**
 * The layout `t` of the way (0 to 1) from `from` to `to`: cards and frames
 * glide, a frame's size with them; what is not a position is `to`'s.
 */
export function blendLayout(
	from: CanvasLayout,
	to: CanvasLayout,
	t: number,
): CanvasLayout {
	const mix = (a: number, b: number) => a + (b - a) * t;
	const nodes: Record<string, Point> = {};
	for (const [id, at] of Object.entries(to.nodes)) {
		const was = from.nodes[id];
		nodes[id] = was ? { x: mix(was.x, at.x), y: mix(was.y, at.y) } : at;
	}
	return {
		...to,
		nodes,
		groups: to.groups.map((group) => {
			const was = from.groups.find((g) => g.id === group.id);
			if (!was) return group;
			return {
				...group,
				x: mix(was.x, group.x),
				y: mix(was.y, group.y),
				w: mix(was.w, group.w),
				h: mix(was.h, group.h),
			};
		}),
	};
}

/**
 * Re-arranges only the given services, left to right by their links, and
 * leaves everything else where it is. The result keeps the selection's
 * top-left corner, so it stays where the user was working. Services that no
 * link joins to another selected one (nothing to rank them by) are placed in
 * a grid below the linked block, in their current reading order. A group
 * frame follows its members.
 */
export function arrangeSelection(
	layout: CanvasLayout,
	links: readonly Link[],
	serviceIds: readonly string[],
	sizeOf: SizeOf = defaultSize,
	traits: LayoutTraits = NO_TRAITS,
): CanvasLayout {
	const ids = [...new Set(serviceIds)]
		.filter((id) => layout.nodes[id] !== undefined)
		.sort(listOrder(traits));
	if (ids.length < 2) return layout;
	const picked = new Set(ids);
	const pairs = links
		.filter(
			(link) =>
				link.fromServiceId !== link.toServiceId &&
				picked.has(link.fromServiceId) &&
				picked.has(link.toServiceId),
		)
		.map((link) => [link.fromServiceId, link.toServiceId] as const);
	const linked = new Set(pairs.flat());
	const connected = ids.filter((id) => linked.has(id));
	const isolated = ids
		.filter((id) => !linked.has(id))
		.sort((a, b) => {
			const pa = layout.nodes[a] as Point;
			const pb = layout.nodes[b] as Point;
			return pa.y - pb.y || pa.x - pb.x;
		});

	// Spots relative to the block's own top-left corner.
	const spots = new Map<string, Point>();
	let blockBottom = 0;
	if (connected.length > 0) {
		const entries = new Set(traits.entryIds);
		const laid = layeredLayout(
			connected.map((id): LayerVertex => {
				const size = sizeOf(id);
				return {
					id,
					w: size.w,
					h: size.h,
					entry: entries.has(id),
					data: traits.dataIds.has(id),
				};
			}),
			pairs,
		);
		for (const [id, at] of laid.boxes) spots.set(id, at);
		blockBottom = laid.height + GAP_Y;
	}
	// A grid whose columns are as wide, and rows as tall, as the cards in them.
	const columns = Math.max(1, Math.ceil(Math.sqrt(isolated.length)));
	const columnWidth: number[] = [];
	const rowHeight: number[] = [];
	isolated.forEach((id, index) => {
		const size = sizeOf(id);
		const column = index % columns;
		const row = Math.floor(index / columns);
		columnWidth[column] = Math.max(columnWidth[column] ?? 0, size.w);
		rowHeight[row] = Math.max(rowHeight[row] ?? 0, size.h);
	});
	const offset = (sizes: readonly number[], upTo: number, gap: number) =>
		sizes.slice(0, upTo).reduce((sum, size) => sum + size + gap, 0);
	isolated.forEach((id, index) => {
		spots.set(id, {
			x: offset(columnWidth, index % columns, GAP_X),
			y: blockBottom + offset(rowHeight, Math.floor(index / columns), GAP_Y),
		});
	});

	let originX = Number.POSITIVE_INFINITY;
	let originY = Number.POSITIVE_INFINITY;
	for (const id of ids) {
		const at = layout.nodes[id] as Point;
		originX = Math.min(originX, at.x);
		originY = Math.min(originY, at.y);
	}
	const nodes: Record<string, Point> = { ...layout.nodes };
	for (const [id, spot] of spots) {
		nodes[id] = roundPoint({ x: originX + spot.x, y: originY + spot.y });
	}
	return fitGroups({ ...layout, nodes }, sizeOf);
}

/** Rounds to whole pixels so persisted layouts do not churn on sub-pixel drags. */
export function roundPoint(p: Point): Point {
	return { x: Math.round(p.x), y: Math.round(p.y) };
}

export type Direction = "left" | "right" | "up" | "down";

/**
 * The node nearest to `from` in the arrow key's direction, or undefined when
 * none lies that way. Candidates must be mostly in that direction; among them
 * the closest centre wins, with the off-axis distance counted double so a
 * neighbour in the same row beats a diagonal one.
 */
export function neighbourInDirection(
	from: string,
	direction: Direction,
	centres: Readonly<Record<string, Point>>,
): string | undefined {
	const origin = centres[from];
	if (!origin) return undefined;
	let best: string | undefined;
	let bestScore = Number.POSITIVE_INFINITY;
	for (const [id, centre] of Object.entries(centres)) {
		if (id === from) continue;
		const dx = centre.x - origin.x;
		const dy = centre.y - origin.y;
		const along =
			direction === "right"
				? dx
				: direction === "left"
					? -dx
					: direction === "down"
						? dy
						: -dy;
		const across = direction === "left" || direction === "right" ? dy : dx;
		if (along <= 0 || Math.abs(across) > along * 2.5) continue;
		const score = along + Math.abs(across) * 2;
		if (score < bestScore) {
			bestScore = score;
			best = id;
		}
	}
	return best;
}
