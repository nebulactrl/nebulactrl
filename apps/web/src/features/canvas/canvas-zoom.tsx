import { useReactFlow, useStore } from "@xyflow/react";
import { memo, useCallback } from "react";
import { ZoomToolbar } from "#/components/canvas/zoom-toolbar";

/**
 * The zoom column bound to the canvas's camera. The camera's zoom lives here
 * and nowhere above: reading it in `CanvasInner` re-rendered the whole canvas,
 * the dock and the inspector whenever it wobbled in the last decimals while
 * a pan eased in. Only the rounded percent the toolbar prints can re-render it.
 */
export const CanvasZoomToolbar = memo(function CanvasZoomToolbar({
	mapOpen,
	onFit,
	onToggleMap,
	onShortcuts,
}: {
	mapOpen: boolean;
	onFit(): void;
	onToggleMap(): void;
	onShortcuts(): void;
}) {
	const rf = useReactFlow();
	const percent = useStore((state) => Math.round(state.transform[2] * 100));
	const zoomIn = useCallback(() => void rf.zoomIn({ duration: 200 }), [rf]);
	const zoomOut = useCallback(() => void rf.zoomOut({ duration: 200 }), [rf]);
	const zoomReset = useCallback(
		() => void rf.zoomTo(1, { duration: 200 }),
		[rf],
	);
	return (
		<ZoomToolbar
			zoom={percent / 100}
			onZoomIn={zoomIn}
			onZoomOut={zoomOut}
			onZoomReset={zoomReset}
			onFit={onFit}
			mapOpen={mapOpen}
			onToggleMap={onToggleMap}
			onShortcuts={onShortcuts}
		/>
	);
});
