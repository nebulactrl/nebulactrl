import { useMemo } from "react";
import type { ServiceLink } from "#/api/models";
import { type ConnectAnchor, linkActions } from "./store";

export interface LinkReviewApi {
	/**
	 * Opens the connect popover for a cable drawn from `fromId` to `toId` at
	 * `at` (the drop point, in screen pixels). It lists the references the
	 * consumer could hold to reach the provider; nothing is written until the
	 * reader confirms, and the cable appears once the variables are staged.
	 * A database is always the provider, whichever end the cable left from.
	 */
	request(fromId: string, toId: string, at?: ConnectAnchor): void;
	/** Lists which variables make `link` and offers to open or remove it. */
	openReferences(link: ServiceLink): void;
}

/** The link flow's entry points, for the canvas (drawing and clicking edges). */
export function useLinkReview(): LinkReviewApi {
	return useMemo(
		() => ({
			request: linkActions.request,
			openReferences: linkActions.openReferences,
		}),
		[],
	);
}
