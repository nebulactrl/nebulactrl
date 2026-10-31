import { useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { deleteTemplateInstance } from "#/api/gen/sdk.gen";
import { invalidateOperations, invalidateServices } from "#/api/invalidate";
import { ConfirmDialog } from "#/components/common/confirm-dialog";
import { CheckLabel } from "#/components/ui/check-label";
import { toast } from "#/components/ui/toast";
import { templatesActions, useTemplatesState } from "./store";

/**
 * Deleting a stack removes every one of its services at once, so it asks for
 * the template id, offers the volumes, and then needs a held press.
 */
export function DeleteStackDialog() {
	const queryClient = useQueryClient();
	const { deleting } = useTemplatesState();
	// Keep the stack while the dialog animates closed.
	const last = useRef(deleting);
	if (deleting) last.current = deleting;
	const instance = last.current;
	const [deleteVolumes, setDeleteVolumes] = useState(false);
	if (!instance) return null;

	const services = (instance.services ?? []).map((service) => service.name);

	return (
		<ConfirmDialog
			open={deleting !== null}
			onOpenChange={(next) => {
				if (next) return;
				templatesActions.cancelDelete();
				setDeleteVolumes(false);
			}}
			glyph="trash"
			danger
			title={`Delete the ${instance.name} stack?`}
			cta="Delete stack"
			typeToConfirm={instance.templateId}
			body={
				<div className="flex flex-col gap-3">
					<p>
						This stops and deletes {services.length}{" "}
						{services.length === 1 ? "service" : "services"} (
						{services.join(", ")}), with their variables, domains, ports and
						deployment history. It cannot be undone.
					</p>
					<CheckLabel
						checked={deleteVolumes}
						onCheckedChange={setDeleteVolumes}
						className="items-start gap-2"
						checkboxClassName="mt-px"
						labelClassName="text-xs leading-[1.5]"
					>
						<span>
							<span className="font-medium text-ink-1">
								Also delete volumes (data is lost)
							</span>
							<span className="block text-ink-3">
								Left unchecked, the volumes stay on the server.
							</span>
						</span>
					</CheckLabel>
				</div>
			}
			onConfirm={async () => {
				await deleteTemplateInstance({
					path: { instanceId: instance.id },
					query: { confirm: instance.templateId, deleteVolumes },
				});
			}}
			onConfirmed={() => {
				toast.success(
					`Deleted the ${instance.name} stack`,
					deleteVolumes
						? "Its services and volumes were removed."
						: "Its services were removed; the volumes were kept on the server.",
				);
				templatesActions.cancelDelete();
				setDeleteVolumes(false);
				void Promise.all([
					invalidateServices(queryClient),
					invalidateOperations(queryClient, [
						"listTemplateInstances",
						"getCanvas",
						"listLinks",
					]),
				]);
			}}
		/>
	);
}
