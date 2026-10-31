import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Trash2Icon } from "lucide-react";
import { errorMessage } from "#/api/client-config";
import { listRetiredVolumesOptions } from "#/api/gen/@tanstack/react-query.gen";
import { deleteRetiredVolume } from "#/api/gen/sdk.gen";
import type { RetiredVolumeView } from "#/api/gen/types.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { Relative } from "#/components/common/relative";
import { Button } from "#/components/ui/button";
import { toast } from "#/components/ui/toast";
import { absoluteTime } from "#/lib/format";
import { invalidateVolumeBackups } from "./queries";

/**
 * The volumes a restore replaced. They stay on the server, unmounted, so the
 * swap can be undone; delete one to free the disk. Renders nothing when there
 * are none.
 */
export function RetiredVolumes({ serviceId }: { serviceId: string }) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const retired = useQuery(listRetiredVolumesOptions({ path: { serviceId } }));
	const remove = useMutation({
		mutationFn: (v: RetiredVolumeView) =>
			deleteRetiredVolume({ path: { retiredId: v.id } }),
		onSuccess: async () => {
			toast.success(
				"Deleting the old volume",
				"The agent removes it; the row disappears when it is gone.",
			);
			await invalidateVolumeBackups(queryClient);
		},
		onError: (e) => toast.error("Could not delete the volume", errorMessage(e)),
	});

	const items = retired.data?.items ?? [];
	if (items.length === 0) return null;
	return (
		<div className="mt-4 flex flex-col gap-1">
			<h4 className="type-panel-title">Retired volumes</h4>
			<p className="type-secondary">
				Replaced by a restore. They are no longer mounted and stay on the server
				until you delete them. Deleting removes their data and cannot be undone.
			</p>
			<ul aria-label="Retired volumes">
				{items.map((v) => (
					<li
						key={v.id}
						className="flex items-center gap-3 border-line border-b py-2 last:border-0"
					>
						<div className="min-w-0 flex-1">
							<span className="block truncate font-mono text-[13px] text-ink-1">
								{v.dockerName}
							</span>
							<span className="block truncate text-ink-4 text-xs">
								{v.volumeName} ·{" "}
								<span title={absoluteTime(v.createdAt)}>
									retired <Relative iso={v.createdAt} />
								</span>
								{v.reason ? ` · ${v.reason}` : ""}
							</span>
						</div>
						<Button
							variant="ghost"
							size="sm"
							className="text-destructive-text"
							disabled={remove.isPending}
							onClick={() =>
								void confirm({
									title: `Delete ${v.dockerName}?`,
									body: `The old volume of ${v.volumeName} and all its data are removed from the server. This cannot be undone. The server must be online.`,
									cta: "Delete volume",
									glyph: "trash",
									danger: true,
									hold: false,
									onConfirm: async () => {
										await remove.mutateAsync(v);
									},
								})
							}
						>
							<Trash2Icon aria-hidden />
							Delete
						</Button>
					</li>
				))}
			</ul>
		</div>
	);
}
