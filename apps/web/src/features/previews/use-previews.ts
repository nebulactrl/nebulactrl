import { useQuery } from "@tanstack/react-query";
import { previewSettingsQuery, previewsQuery } from "./queries";

/** The project's previews; polls while one is changing (the event feed is the primary signal). */
export function usePreviews(projectId: string | undefined) {
	return useQuery({
		...previewsQuery(projectId ?? ""),
		enabled: projectId !== undefined,
		refetchInterval: (query) =>
			query.state.data?.items?.some((p) =>
				["creating", "deploying", "seeding", "deleting"].includes(p.status),
			)
				? 4000
				: 15_000,
	});
}

export function usePreviewSettings(projectId: string) {
	return useQuery(previewSettingsQuery(projectId));
}
