import { useOnViewportChange, useReactFlow } from "@xyflow/react";
import { type RefObject, useCallback, useEffect } from "react";
import { type FloorViewport, latticeLayer } from "./lib/floor";

/**
 * Makes the floor's lattice layer follow the viewport. A pan is one
 * `transform` write on that layer (the compositor moves it, nothing is
 * painted); only a zoom also writes `background-size`. The writes go straight
 * to the one element, synchronously with the viewport change so the dots and
 * the cards land in the same frame, and never touch an ancestor of the cards:
 * a changed custom property on one would restyle every node on every frame.
 * No React state moves, nothing re-renders.
 */
export function useCanvasFloor(latticeRef: RefObject<HTMLElement | null>) {
	const rf = useReactFlow();

	const follow = useCallback(
		(viewport: FloorViewport) => {
			const el = latticeRef.current;
			if (!el) return;
			const { pitch, x, y } = latticeLayer(viewport);
			el.style.transform = `translate3d(${x}px, ${y}px, 0)`;
			const size = `${pitch}px`;
			// Only a zoom changes the pitch, and a changed size repaints the layer.
			if (el.dataset.pitch !== size) {
				el.dataset.pitch = size;
				el.style.backgroundSize = `${size} ${size}`;
			}
		},
		[latticeRef],
	);

	useOnViewportChange({ onChange: follow });

	// The viewport the canvas opened with moves nobody: place it once.
	useEffect(() => {
		follow(rf.getViewport());
	}, [rf, follow]);
}
