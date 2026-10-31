import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useRef } from "react";
import { errorMessage, errorStatus } from "#/api/client-config";
import { getCanvas, getService, putCanvas } from "#/api/gen/sdk.gen";
import { canvasQuery } from "#/api/hooks";
import {
	type CanvasDocument,
	type CanvasLayout,
	fromCanvasLayout,
	type Point,
	toCanvasDocument,
} from "#/api/models";
import { toast } from "#/components/ui/toast";

/** How long a drag must rest before the layout is written back. */
export const SAVE_DEBOUNCE_MS = 500;

const ERROR_TOAST_GAP_MS = 60_000;

// --- placing a node from outside the canvas -------------------------------

type Placer = (serviceId: string, position: Point) => void;
let activePlacer: Placer | null = null;

/**
 * The mounted canvas takes placements itself, so its own layout state (and
 * its next `If-Match`) never falls behind a write made behind its back.
 * Returns the function that lets go.
 */
export function registerPlacer(placer: Placer): () => void {
	activePlacer = placer;
	return () => {
		if (activePlacer === placer) activePlacer = null;
	};
}

/**
 * Puts a service's card at `position` (canvas coordinates). With a canvas on
 * screen it moves the card there; otherwise it writes the saved layout of the
 * service's environment directly, retrying once on a stale `If-Match`.
 */
export async function placeNode(
	serviceId: string,
	position: Point,
): Promise<void> {
	if (activePlacer) {
		activePlacer(serviceId, position);
		return;
	}
	const { data: service } = await getService({ path: { serviceId } });
	const path = {
		projectId: service.projectId,
		environmentId: service.environmentId,
	};
	for (let attempt = 0; ; attempt += 1) {
		const { data } = await getCanvas({ path });
		const current = toCanvasDocument(data);
		try {
			await putCanvas({
				path,
				body: {
					layout: fromCanvasLayout({
						...current.layout,
						nodes: { ...current.layout.nodes, [serviceId]: position },
					}),
				},
				headers: { "If-Match": `"${current.version}"` },
			});
			return;
		} catch (error) {
			if (errorStatus(error) !== 412 || attempt > 0) throw error;
		}
	}
}

export interface LayoutPersistence {
	/** Queues `layout` to be written after the debounce, replacing any queued one. */
	schedule(layout: CanvasLayout): void;
	/** Writes any queued layout now. */
	flush(): Promise<void>;
	/** Takes a layout the server saved elsewhere: drops queued writes and adopts its version. */
	adopt(document: CanvasDocument): void;
}

interface Options {
	projectId: string;
	environmentId: string;
	/** The version the loaded layout came with (`If-Match`). */
	initialVersion: number;
	/**
	 * The server's layout won a conflict that a retry could not resolve:
	 * the canvas should show it.
	 */
	onAdopt(layout: CanvasLayout): void;
}

/**
 * Debounced `PUT …/canvas` with `If-Match`. A 412 refetches the current
 * version and writes the local layout again on top of it; a second conflict
 * means someone else keeps editing, so the server's layout is adopted.
 * Writes are serialised so versions never race.
 */
export function useLayoutPersistence({
	projectId,
	environmentId,
	initialVersion,
	onAdopt,
}: Options): LayoutPersistence {
	const queryClient = useQueryClient();
	const versionRef = useRef(initialVersion);
	const pendingRef = useRef<CanvasLayout | null>(null);
	const timerRef = useRef<number | undefined>(undefined);
	const chainRef = useRef<Promise<void>>(Promise.resolve());
	const lastErrorToast = useRef(0);
	const onAdoptRef = useRef(onAdopt);
	onAdoptRef.current = onAdopt;

	const store = useCallback(
		(document: CanvasDocument) => {
			versionRef.current = document.version;
			queryClient.setQueryData(
				canvasQuery(projectId, environmentId).queryKey,
				document,
			);
		},
		[queryClient, projectId, environmentId],
	);

	const fetchLatest = useCallback(async (): Promise<CanvasDocument> => {
		const { data } = await getCanvas({ path: { projectId, environmentId } });
		return toCanvasDocument(data);
	}, [projectId, environmentId]);

	const put = useCallback(
		async (layout: CanvasLayout): Promise<CanvasDocument> => {
			const { data } = await putCanvas({
				path: { projectId, environmentId },
				body: { layout: fromCanvasLayout(layout) },
				headers: { "If-Match": `"${versionRef.current}"` },
			});
			return toCanvasDocument(data);
		},
		[projectId, environmentId],
	);

	const write = useCallback(
		async (layout: CanvasLayout): Promise<void> => {
			try {
				store(await put(layout));
				return;
			} catch (error) {
				if (errorStatus(error) !== 412) throw error;
			}
			// Someone else saved first: take their version, then write ours on top.
			const latest = await fetchLatest();
			versionRef.current = latest.version;
			try {
				store(await put(layout));
			} catch (error) {
				if (errorStatus(error) !== 412) throw error;
				const fresh = await fetchLatest();
				store(fresh);
				onAdoptRef.current(fresh.layout);
			}
		},
		[fetchLatest, put, store],
	);

	const flush = useCallback((): Promise<void> => {
		window.clearTimeout(timerRef.current);
		timerRef.current = undefined;
		const layout = pendingRef.current;
		pendingRef.current = null;
		if (!layout) return chainRef.current;
		chainRef.current = chainRef.current.then(() =>
			write(layout).catch((error: unknown) => {
				const now = Date.now();
				if (now - lastErrorToast.current < ERROR_TOAST_GAP_MS) return;
				lastErrorToast.current = now;
				toast.error("Couldn't save the canvas layout", errorMessage(error));
			}),
		);
		return chainRef.current;
	}, [write]);

	const schedule = useCallback(
		(layout: CanvasLayout) => {
			pendingRef.current = layout;
			window.clearTimeout(timerRef.current);
			timerRef.current = window.setTimeout(() => {
				void flush();
			}, SAVE_DEBOUNCE_MS);
		},
		[flush],
	);

	// Leaving the canvas writes whatever is still queued.
	useEffect(() => () => void flush(), [flush]);

	/** A layout saved by someone else (a template install) replaces what is queued. */
	const adopt = useCallback(
		(document: CanvasDocument) => {
			window.clearTimeout(timerRef.current);
			timerRef.current = undefined;
			pendingRef.current = null;
			store(document);
			onAdoptRef.current(document.layout);
		},
		[store],
	);

	return useMemo(() => ({ schedule, flush, adopt }), [schedule, flush, adopt]);
}
