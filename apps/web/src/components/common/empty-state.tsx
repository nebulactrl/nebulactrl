import {
	DitherEmptyState,
	type DitherEmptyStateProps,
} from "@nebulactrl/dither/interface/empty-state";
import type { EmptyStateIllustration } from "@nebulactrl/dither/interface/illustration";

export type { EmptyStateIllustration };

export interface EmptyStateProps
	extends Omit<
		DitherEmptyStateProps,
		"illustration" | "icon" | "media" | "backdrop" | "animate"
	> {
	/**
	 * The page's object, drawn in static dither: `box` services and previews,
	 * `rack` servers, `radar` signals and logs, `key` credentials, `network`
	 * links and teams, `bell` notifications, `folder` records and history,
	 * `search` a filter with no match, `planet` projects.
	 */
	illustration: EmptyStateIllustration;
}

/**
 * Every empty state in the app: the dither kit's `DitherEmptyState` in the app
 * tier. A small static dithered object, no backdrop and no
 * bob; the title, the sentence and the action stay solid. It invites: a title
 * that names the next step, a sentence of why, one primary and one secondary
 * action, and the shortcuts that do the same.
 */
export function EmptyState({ size = "md", ...props }: EmptyStateProps) {
	return (
		<DitherEmptyState {...props} size={size} backdrop={false} animate={false} />
	);
}
