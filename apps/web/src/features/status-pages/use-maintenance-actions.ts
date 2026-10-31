import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import {
	cancelMaintenanceWindow,
	deleteMaintenanceWindow,
} from "#/api/gen/sdk.gen";
import type { MaintenanceWindowView } from "#/api/gen/types.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { toast } from "#/components/ui/toast";
import { invalidateStatusPages } from "./queries";

export interface MaintenanceActions {
	/** The window the edit dialog is open on, if any. */
	editing: MaintenanceWindowView | null;
	closeEditor: () => void;
	onEdit: (window: MaintenanceWindowView) => void;
	onCancel: (window: MaintenanceWindowView) => void;
	onDelete: (window: MaintenanceWindowView) => void;
}

/**
 * What the maintenance rows can do to a window: open it in the edit dialog,
 * end or cancel it, delete it. Ending and deleting ask first; the page that
 * uses this renders the dialog on `editing`.
 */
export function useMaintenanceActions(): MaintenanceActions {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const [editing, setEditing] = useState<MaintenanceWindowView | null>(null);

	return {
		editing,
		closeEditor: () => setEditing(null),
		onEdit: setEditing,
		onCancel: (window) => {
			const running = window.phase === "active";
			void confirm({
				title: running
					? `End “${window.title}” now?`
					: `Cancel “${window.title}”?`,
				body: running
					? "Its services leave maintenance right away and the pages stop announcing it. The window stays in the past list."
					: "The window will not start and the pages stop announcing it. It stays in the past list.",
				cta: running ? "End maintenance" : "Cancel maintenance",
				dismiss: "Keep it",
				danger: true,
				hold: false,
				onConfirm: async () => {
					await cancelMaintenanceWindow({
						path: { windowId: window.id },
						throwOnError: true,
					});
					await invalidateStatusPages(queryClient);
					toast.success(
						running ? "Maintenance ended" : "Maintenance canceled",
						"The pages no longer announce it.",
					);
				},
			});
		},
		onDelete: (window) => {
			void confirm({
				title: `Delete “${window.title}”?`,
				body: "The window is removed from every page and from the list. If it is running, its services leave maintenance. This cannot be undone.",
				cta: "Delete window",
				danger: true,
				hold: false,
				onConfirm: async () => {
					await deleteMaintenanceWindow({
						path: { windowId: window.id },
						throwOnError: true,
					});
					await invalidateStatusPages(queryClient);
					toast.success("Maintenance deleted", "It is gone from the pages.");
				},
			});
		},
	};
}
