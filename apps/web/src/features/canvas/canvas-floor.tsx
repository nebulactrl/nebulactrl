import { memo, type Ref } from "react";

/**
 * The canvas floor: the halftone dawn and vignette (static, painted once)
 * with the dot lattice on its own layer above them. It is a sibling of the
 * React Flow canvas, never an ancestor: the lattice moves with the viewport
 * (`useCanvasFloor`), and anything written to an ancestor of the cards would
 * restyle all of them. Without a `latticeRef` the lattice rests at the origin.
 */
export const CanvasFloor = memo(function CanvasFloor({
	production,
	latticeRef,
}: {
	production: boolean;
	latticeRef?: Ref<HTMLDivElement>;
}) {
	return (
		<div
			aria-hidden
			className="canvas-floor pointer-events-none absolute inset-0 overflow-hidden"
			data-env={production ? "production" : undefined}
		>
			<div ref={latticeRef} className="canvas-lattice" />
		</div>
	);
});
