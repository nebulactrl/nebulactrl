import { useMutation, useQueryClient } from "@tanstack/react-query";
import { CheckCheckIcon, EyeIcon } from "lucide-react";
import { useState } from "react";
import { errorCode, errorMessage } from "#/api/client-config";
import {
	applyMeshAclMutation,
	checkMeshAclMutation,
	updateMeshSettingsMutation,
} from "#/api/gen/@tanstack/react-query.gen";
import { updateMeshSettings } from "#/api/gen/sdk.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { CopyButton } from "#/components/common/copy-button";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { KvRow } from "#/components/ui/kv-row";
import { Segmented } from "#/components/ui/segmented";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { AclPreviewDialog } from "./acl-preview-dialog";
import {
	ACL_MODES,
	type AclMode,
	aclChip,
	aclModeNote,
	type MeshView,
	meshErrorText,
} from "./model";
import { invalidateMesh } from "./queries";
import { StateLabel } from "./state-label";

const MANAGED_PUSH_BODY =
	"NebulaCtrl will write the mesh rules into your tailnet's policy file. That needs the policy_file scope on the Tailscale OAuth client, and anyone who controls this control plane could then rewrite the whole policy, not only the mesh rules. Only rules that name mesh node addresses are replaced; your other rules and comments are kept. Turning this on is recorded in the audit log.";

/**
 * How the tailnet's access policy is handled: the mode (advisory by default),
 * its state, and the actions that fit it. Choosing Managed on Tailscale is an
 * opt-in with its trade-off spelled out first. Only an admin changes it.
 */
export function AclCard({
	mesh,
	isAdmin,
}: {
	mesh: MeshView;
	isAdmin: boolean;
}) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const [previewing, setPreviewing] = useState(false);
	const chip = aclChip(mesh.aclMode, mesh.aclState);
	const note = aclModeNote(mesh.provider, mesh.aclMode);
	const managedBlocked = !mesh.managedPushAvailable;

	const setMode = useMutation({
		...updateMeshSettingsMutation(),
		onSuccess: async (_data, variables) => {
			const aclMode = variables.body.aclMode;
			await invalidateMesh(queryClient);
			toast.success(
				`Access policy: ${ACL_MODES.find((mode) => mode.id === aclMode)?.label.toLowerCase()}`,
			);
		},
		onError: (error) =>
			toast.error(
				"Could not change the access policy mode",
				meshErrorText(errorCode(error), errorMessage(error)),
			),
	});

	const apply = useMutation({
		...applyMeshAclMutation(),
		onSuccess: async () => {
			await invalidateMesh(queryClient);
			toast.success("Rules written", "The tailnet policy now carries them.");
		},
		onError: (error) =>
			toast.error(
				"Could not write the rules",
				meshErrorText(errorCode(error), errorMessage(error)),
			),
	});

	const check = useMutation({
		...checkMeshAclMutation(),
		onSuccess: async () => {
			await invalidateMesh(queryClient);
			toast.success("Checked", "The state above is current.");
		},
		onError: (error) =>
			toast.error(
				"Could not check the rules",
				meshErrorText(errorCode(error), errorMessage(error)),
			),
	});

	const choose = (next: AclMode) => {
		if (next === mesh.aclMode) return;
		if (next === "managed" && mesh.provider === "tailscale") {
			void confirm({
				title: "Let NebulaCtrl write your Tailscale policy?",
				body: MANAGED_PUSH_BODY,
				cta: "Write the policy",
				danger: true,
				hold: false,
				onConfirm: async () => {
					try {
						await updateMeshSettings({ body: { aclMode: "managed" } });
					} catch (error) {
						throw new Error(
							meshErrorText(errorCode(error), errorMessage(error)),
						);
					}
					await invalidateMesh(queryClient);
					toast.success("Access policy: managed");
				},
			});
			return;
		}
		setMode.mutate({ body: { aclMode: next } });
	};

	return (
		<div className="rule-rows">
			<KvRow
				label="Access policy"
				description="Who may reach which port over the mesh. Generated from your servers, links and grants; never written by hand."
			>
				<div className="flex flex-col gap-2">
					<Segmented
						aria-label="Access policy mode"
						font="sans"
						density="regular"
						value={mesh.aclMode}
						onValueChange={choose}
						options={ACL_MODES.map((mode) => ({
							value: mode.id,
							label: mode.label,
							disabled:
								!isAdmin ||
								setMode.isPending ||
								(mode.id === "managed" && managedBlocked),
						}))}
					/>
					<p className="text-ink-3 text-xs leading-[1.5]">
						{ACL_MODES.find((mode) => mode.id === mesh.aclMode)?.hint}
					</p>
					{note ? (
						<p className="text-ink-3 text-xs leading-[1.5]">{note}</p>
					) : null}
					{managedBlocked ? (
						<p className="text-ink-3 text-xs leading-[1.5]">
							Managed is not available: this {mesh.provider} credential cannot
							write the tailnet policy
							{mesh.provider === "tailscale"
								? " (the OAuth client needs the policy_file scope)"
								: ""}
							. Use advisory and add the rules yourself.
						</p>
					) : null}
					{isAdmin ? null : (
						<p className="text-ink-3 text-xs">
							Changing the access policy needs the admin scope.
						</p>
					)}
				</div>
			</KvRow>
			<KvRow label="State">
				<div className="flex flex-col gap-1.5">
					<StateLabel chip={chip} />
					{mesh.aclMessage ? (
						<p className="text-ink-3 text-xs leading-[1.5]">
							{mesh.aclMessage}
						</p>
					) : null}
				</div>
			</KvRow>
			{mesh.aclMode === "managed" && mesh.aclManagedWarning ? (
				<div className="py-3.5">
					<Alert variant="warning">
						<AlertDescription>{mesh.aclManagedWarning}</AlertDescription>
					</Alert>
				</div>
			) : null}
			<div className="flex flex-wrap items-center gap-2 py-3.5">
				<Button variant="secondary" onClick={() => setPreviewing(true)}>
					<EyeIcon aria-hidden />
					Preview rules
				</Button>
				{mesh.aclMode === "advisory" && mesh.snippet ? (
					<CopyButton value={mesh.snippet}>Copy snippet</CopyButton>
				) : null}
				<Button
					variant="secondary"
					disabled={check.isPending || mesh.aclMode === "off"}
					onClick={() => check.mutate({})}
				>
					{check.isPending ? <Spinner /> : <CheckCheckIcon aria-hidden />}
					Check now
				</Button>
				{mesh.aclMode === "managed" && isAdmin ? (
					<Button
						variant="secondary"
						disabled={apply.isPending}
						onClick={() => apply.mutate({})}
					>
						{apply.isPending ? <Spinner /> : null}
						Write rules now
					</Button>
				) : null}
			</div>
			<AclPreviewDialog
				open={previewing}
				onOpenChange={setPreviewing}
				mesh={mesh}
			/>
		</div>
	);
}
