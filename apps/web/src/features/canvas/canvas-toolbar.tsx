import {
	NetworkIcon,
	SearchIcon,
	SquareDashedMousePointerIcon,
} from "lucide-react";
import { memo } from "react";
import { Button } from "#/components/ui/button";
import { Kbd } from "#/components/ui/kbd";

/**
 * Search, Arrange and Group, in the canvas's top-left corner. A component of
 * its own so the camera, the selection and the inspector re-rendering the
 * canvas leave these three buttons alone. With little free canvas beside a
 * wide inspector (`compact`) the labels go and the three keys are icons, so
 * the bar and Create fit the free area instead of running under the sheet.
 */
export const CanvasToolbar = memo(function CanvasToolbar({
	empty,
	canGroup,
	compact,
	onSearch,
	onArrange,
	onGroup,
}: {
	empty: boolean;
	canGroup: boolean;
	compact: boolean;
	onSearch(): void;
	onArrange(): void;
	onGroup(): void;
}) {
	const size = compact ? "icon-sm" : "sm";
	// Icon keys grow to 36px under a finger.
	const keyClass = compact ? "pointer-coarse:size-9" : undefined;
	return (
		<div className="surface-float pointer-events-auto flex items-center gap-0.5 rounded-ctl p-1">
			<Button
				variant="ghost"
				size={size}
				className={keyClass}
				onClick={onSearch}
				title="Search (⌘F)"
				aria-label="Search services"
			>
				<SearchIcon aria-hidden />
				{compact ? null : (
					<>
						Search
						<Kbd variant="key" className="ml-1 max-sm:hidden">
							⌘F
						</Kbd>
					</>
				)}
			</Button>
			<Button
				variant="ghost"
				size={size}
				className={keyClass}
				onClick={onArrange}
				disabled={empty}
				title="Arrange services left to right"
				aria-label={compact ? "Arrange services" : undefined}
			>
				<NetworkIcon aria-hidden />
				{compact ? null : "Arrange"}
			</Button>
			<Button
				variant="ghost"
				size={size}
				className={keyClass}
				onClick={onGroup}
				disabled={!canGroup}
				title="Group the selected services (select several with Shift)"
				aria-label={compact ? "Group selected services" : undefined}
			>
				<SquareDashedMousePointerIcon aria-hidden />
				{compact ? null : "Group"}
			</Button>
		</div>
	);
});
