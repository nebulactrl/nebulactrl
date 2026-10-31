import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { errorCode, errorMessage } from "#/api/client-config";
import { deletePlatformService } from "#/api/gen/sdk.gen";
import type { PlatformServiceView } from "#/api/gen/types.gen";
import { ConfirmDialog } from "#/components/common/confirm-dialog";
import { CheckLabel } from "#/components/ui/check-label";
import { toast } from "#/components/ui/toast";
import { REMOVAL_IMPACT, removalBlocker, removeQuery } from "./model";
import { invalidatePlatform } from "./queries";

/**
 * Remove the team's Headscale. The kind is typed to confirm; the Tailscale
 * integration it feeds goes first, so a refusal (a tailnet in use) leaves
 * everything as it was and shows the server's reason verbatim.
 */
export function RemoveDialog({
	view,
	label,
	open,
	onOpenChange,
}: {
	view: Pick<PlatformServiceView, "id" | "kind">;
	label: string;
	open: boolean;
	onOpenChange(open: boolean): void;
}) {
	const queryClient = useQueryClient();
	const [deleteData, setDeleteData] = useState(false);
	useEffect(() => {
		if (open) setDeleteData(false);
	}, [open]);

	const remove = async () => {
		try {
			await deletePlatformService({
				path: { id: view.id },
				query: removeQuery({ deleteData }),
				throwOnError: true,
			});
		} catch (error) {
			const blocker = removalBlocker(errorCode(error), errorMessage(error));
			throw new Error(
				blocker.hint ? `${blocker.message} ${blocker.hint}` : blocker.message,
			);
		}
		toast.success(
			`Removed ${label}`,
			deleteData
				? "Its services and data were deleted."
				: "Its data volume stays on the server.",
		);
		await invalidatePlatform(queryClient);
	};

	return (
		<ConfirmDialog
			open={open}
			onOpenChange={onOpenChange}
			title={`Remove ${label}?`}
			cta="Remove"
			danger
			hold={false}
			typeToConfirm={view.kind}
			onConfirm={remove}
			body={
				<div className="flex flex-col gap-3">
					<p className="m-0">
						NebulaCtrl removes {REMOVAL_IMPACT} first, then stops and deletes
						the service. Nothing is removed if that first step is refused.
					</p>
					<CheckLabel
						checked={deleteData}
						onCheckedChange={setDeleteData}
						className="items-start"
						labelClassName="text-ink-1 text-xs leading-relaxed"
						checkboxClassName="mt-0.5"
					>
						Also delete its data. Without this the data volume stays on the
						server and can be reused.
					</CheckLabel>
				</div>
			}
		/>
	);
}
