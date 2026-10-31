import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { errorMessage } from "#/api/client-config";
import { deleteRoute, deleteService } from "#/api/gen/sdk.gen";
import { invalidateServices } from "#/api/invalidate";
import type { Service } from "#/api/models";
import { HoldActionButton } from "#/components/motion/hold-action-button";
import { useProjectContext } from "#/components/shell/use-project-context";
import { CheckLabel } from "#/components/ui/check-label";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { useInspectorActions } from "#/features/canvas/inspector/inspector-context";
import { deleteDescription, holdLabel } from "./danger-copy";
import { Section, SettingRow } from "./fields";

/**
 * Deleting a service: type its slug, say whether its Docker volumes go too,
 * then hold the button. The three steps are the confirmation; nothing here
 * is undoable.
 */
export function DangerZone({ service }: { service: Service }) {
	const queryClient = useQueryClient();
	const { close } = useInspectorActions();
	const [typed, setTyped] = useState("");
	const [deleteVolumes, setDeleteVolumes] = useState(false);
	const matches = typed.trim() === service.slug;
	const isRoute = service.kind === "route";
	const environment = useProjectContext()?.environments.find(
		(candidate) => candidate.id === service.environmentId,
	);

	const remove = useMutation({
		mutationFn: async () => {
			if (isRoute) {
				await deleteRoute({ path: { routeId: service.id } });
				return;
			}
			await deleteService({
				path: { serviceId: service.id },
				query: { confirm: service.slug, deleteVolumes },
			});
		},
		onSuccess: async () => {
			toast.success(
				`Deleted ${service.name}`,
				deleteVolumes
					? "Its volumes were removed."
					: "Its volumes were kept on the server.",
			);
			// The inspector shows a service that no longer exists: close it.
			close();
			await invalidateServices(queryClient);
		},
	});

	return (
		<Section
			title="Danger zone"
			tone="danger"
			description={deleteDescription({
				serviceName: service.name,
				isRoute,
				environment,
			})}
		>
			<SettingRow
				label="Confirm the slug"
				htmlFor="delete-confirm"
				description={
					<>
						Type <span className="font-mono text-ink-1">{service.slug}</span> to
						enable deleting.
					</>
				}
			>
				<Input
					id="delete-confirm"
					autoComplete="off"
					spellCheck={false}
					className="max-w-[320px] font-mono"
					placeholder={service.slug}
					value={typed}
					onChange={(event) => setTyped(event.target.value)}
				/>
			</SettingRow>
			{isRoute ? null : (
				<SettingRow
					label="Volumes"
					description="What happens to the data the service stored."
				>
					<CheckLabel
						checked={deleteVolumes}
						onCheckedChange={setDeleteVolumes}
						className="items-start gap-2"
						checkboxClassName="mt-px"
						labelClassName="text-xs leading-[1.5]"
					>
						<span>
							<span className="font-medium text-ink-1">
								Also delete the service's volumes
							</span>
							<span className="block text-ink-3">
								Their data is lost. Left unchecked, the volumes stay on the
								server.
							</span>
						</span>
					</CheckLabel>
				</SettingRow>
			)}
			<SettingRow label={`Delete ${service.name}`}>
				<div className="flex flex-wrap items-center gap-3">
					<HoldActionButton
						type="horizontal"
						holdDuration={1300}
						disabled={remove.isPending}
						refused={!matches}
						holdingLabel="Keep holding…"
						completeLabel={
							remove.isPending ? (
								<>
									<Spinner data-icon="inline-start" />
									Deleting
								</>
							) : (
								"Deleted"
							)
						}
						onHoldComplete={() => remove.mutate()}
						fillClassName="bg-destructive-deep"
						labelClassName="text-[13px] leading-none"
						activeLabelClassName="text-inverse-hover"
						className="h-[34px] min-w-0 px-4 font-semibold"
					>
						{holdLabel({ isRoute, environment })}
					</HoldActionButton>
					{!matches ? (
						<span className="type-secondary">Type the slug first.</span>
					) : null}
				</div>
				{remove.isError ? (
					<FieldError>{errorMessage(remove.error)}</FieldError>
				) : null}
			</SettingRow>
		</Section>
	);
}
