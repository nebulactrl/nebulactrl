import {
	queryOptions,
	useMutation,
	useQueryClient,
} from "@tanstack/react-query";
import {
	getPreviewSettingsOptions,
	listPreviewsOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import { deletePreview, updatePreviewSettings } from "#/api/gen/sdk.gen";
import type { PreviewView } from "#/api/gen/types.gen";
import { invalidateOperations } from "#/api/invalidate";
import { toast } from "#/components/ui/toast";
import { draftToBody, type SettingsDraft } from "./model";

/** Everything the previews UI shows; refetched after any change. */
const PREVIEW_OPERATIONS = [
	"listPreviews",
	"getPreviewSettings",
	"listEnvironments",
] as const;

export const previewsQuery = (projectId: string) =>
	queryOptions({
		...listPreviewsOptions({ path: { projectId } }),
		select: (list) => list.items ?? [],
	});

export const previewSettingsQuery = (projectId: string) =>
	getPreviewSettingsOptions({ path: { projectId } });

/** Saves the project's preview settings. */
export function useSavePreviewSettings(projectId: string) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (draft: SettingsDraft) => {
			const { data } = await updatePreviewSettings({
				path: { projectId },
				body: draftToBody(draft),
				throwOnError: true,
			});
			return data;
		},
		onSuccess: async (settings) => {
			await invalidateOperations(queryClient, PREVIEW_OPERATIONS);
			toast.success(
				settings.enabled ? "Previews are on" : "Previews are off",
				settings.enabled
					? "Every pull request of a connected repository gets its own environment."
					: "Existing previews keep running until their pull requests close.",
			);
		},
	});
}

/** Deletes a preview: its services, volumes and domains, then the environment. */
export function useDeletePreview() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (preview: PreviewView) => {
			await deletePreview({
				path: { previewId: preview.id },
				throwOnError: true,
			});
			return preview;
		},
		onSuccess: async (preview) => {
			await invalidateOperations(queryClient, PREVIEW_OPERATIONS);
			toast.success(
				`Deleting PR #${preview.prNumber}`,
				"Its containers, volumes and domains are being removed.",
			);
		},
	});
}
