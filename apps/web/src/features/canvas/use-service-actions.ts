import {
	BanIcon,
	ConstructionIcon,
	CopyIcon,
	FolderOpenIcon,
	HammerIcon,
	type LucideIcon,
	PlayIcon,
	RefreshCwIcon,
	RocketIcon,
	SquareIcon,
	TerminalIcon,
	TerminalSquareIcon,
	Trash2Icon,
	WorkflowIcon,
} from "lucide-react";
import { useMemo } from "react";
import type { Service } from "#/api/models";
import { useConfirm } from "#/components/common/confirm-dialog";
import { toast } from "#/components/ui/toast";
import { openStagedReview } from "#/features/deploy-button";
import { useDeploymentActions } from "#/features/deployments/use-deployment-actions";
import {
	isInMaintenance,
	maintenanceSupported,
	openMaintenance,
	useSetMaintenance,
} from "#/features/maintenance";
import { useStackNavigation } from "#/features/stacks/url";
import { openRunCommand } from "#/features/tasks";
import { openFiles, openShell } from "#/features/terminal";
import { deployLabel } from "./lib/deploy-label";
import { canCancel } from "./lib/status";

export type ServiceActionId =
	| "redeploy"
	| "redeploy-no-cache"
	| "restart"
	| "toggle"
	| "cancel"
	| "maintenance"
	| "run-command"
	| "shell"
	| "files"
	| "copy-host"
	| "processes"
	| "delete";

/** One row of the dock's More menu and the card's context menu. */
export interface ServiceAction {
	id: ServiceActionId;
	label: string;
	icon: LucideIcon;
	run(): void;
	disabled: boolean;
	destructive: boolean;
}

export interface ServiceActions {
	/**
	 * Deploy this service. In production it opens the top bar's review sheet
	 * scoped to the service, where the guarded key is the one confirmation
	 * (staged or not); in any other environment it deploys at once.
	 */
	deploy(): void;
	/** The rows of the More menu, in order; an action that cannot apply is left out. */
	menu: ServiceAction[];
	/** A request of these actions is on its way. */
	busy: boolean;
}

/**
 * What the dock and the context menu do to one service. The requests and
 * their toasts come from `useDeploymentActions`; this only decides which
 * door each action goes through.
 */
export function useServiceActions(
	service: Service,
	{
		isProduction,
		onDelete,
	}: {
		isProduction: boolean;
		/** Opens the place a service is deleted: the inspector's danger zone. */
		onDelete(): void;
	},
): ServiceActions {
	const actions = useDeploymentActions(service);
	const confirm = useConfirm();
	const { deploy, confirmDeployNoCache, restart, stop, cancel } = actions;
	const maintenance = useSetMaintenance(service);
	const stopped = service.desiredState === "stopped";
	const { show } = useStackNavigation();

	return useMemo<ServiceActions>(() => {
		// Production has one confirmation: the guarded key in the top bar's
		// review sheet, scoped to this service. Elsewhere a deploy is immediate.
		const deployNow = () => {
			if (isProduction) {
				openStagedReview({ serviceId: service.id });
				return;
			}
			deploy.mutate(undefined);
		};

		const menu: ServiceAction[] = [
			{
				id: "redeploy",
				label: deployLabel(service),
				icon: RocketIcon,
				run: deployNow,
				disabled: deploy.isPending,
				destructive: false,
			},
			stopped
				? {
						id: "toggle",
						label: "Start",
						icon: PlayIcon,
						run: () => restart.mutate(),
						disabled: restart.isPending || service.currentDeploymentId === null,
						destructive: false,
					}
				: {
						id: "restart",
						label: "Restart",
						icon: RefreshCwIcon,
						run: () => restart.mutate(),
						disabled: restart.isPending || service.currentDeploymentId === null,
						destructive: false,
					},
		];
		// A clean rebuild only means something for a git source; the confirmation
		// explains the cost, in production too.
		if (service.sourceType === "git") {
			menu.push({
				id: "redeploy-no-cache",
				label: "Redeploy without cache…",
				icon: HammerIcon,
				run: () => void confirmDeployNoCache(),
				disabled: deploy.isPending || actions.deployNoCache.isPending,
				destructive: false,
			});
		}
		if (!stopped && service.currentDeploymentId !== null) {
			menu.push({
				id: "toggle",
				label: "Stop",
				icon: SquareIcon,
				run: () => stop.mutate(),
				disabled: stop.isPending,
				destructive: false,
			});
		}
		const latest = service.latestDeployment;
		if (latest && canCancel(service)) {
			menu.push({
				id: "cancel",
				label: "Cancel deployment",
				icon: BanIcon,
				run: () =>
					void confirm({
						title: `Cancel this deployment of ${service.name}?`,
						body: "The deployment stops at the step it has reached: a build is killed, the new containers are removed and the previous routes are put back. The current release keeps serving.",
						cta: "Cancel deployment",
						dismiss: "Keep running",
						onConfirm: async () => {
							await cancel.mutateAsync(latest.id).catch(() => undefined);
						},
					}),
				disabled: false,
				destructive: false,
			});
		}
		if (maintenanceSupported(service)) {
			const on = isInMaintenance(service);
			menu.push({
				id: "maintenance",
				label: on ? "End maintenance" : "Maintenance mode…",
				icon: ConstructionIcon,
				run: on
					? () => maintenance.mutate({ enabled: false })
					: () => openMaintenance(service.id),
				disabled: maintenance.isPending,
				destructive: false,
			});
		}
		if (service.currentDeploymentId !== null && service.kind !== "route") {
			menu.push({
				id: "run-command",
				label: "Run command…",
				icon: TerminalIcon,
				run: () => openRunCommand(service.id),
				disabled: false,
				destructive: false,
			});
			menu.push(
				{
					id: "shell",
					label: "Open shell…",
					icon: TerminalSquareIcon,
					run: () => openShell(service.id),
					disabled: false,
					destructive: false,
				},
				{
					id: "files",
					label: "Browse files…",
					icon: FolderOpenIcon,
					run: () => openFiles(service.id),
					disabled: false,
					destructive: false,
				},
			);
		}
		// Processes run from the service's image (git or image source); a stack, a
		// database or a route has none to add.
		if (
			(service.kind === "app" || service.kind === "worker") &&
			(service.sourceType === "git" || service.sourceType === "image")
		) {
			menu.push({
				id: "processes",
				label: "Open processes…",
				icon: WorkflowIcon,
				run: () =>
					show({
						service: service.id,
						tab: "settings",
						section: "processes",
						component: null,
						process: null,
					}),
				disabled: false,
				destructive: false,
			});
		}
		menu.push(
			{
				id: "copy-host",
				label: "Copy private host",
				icon: CopyIcon,
				run: () => {
					void navigator.clipboard
						.writeText(service.privateHost)
						.then(() => toast.success("Copied", service.privateHost));
				},
				disabled: false,
				destructive: false,
			},
			{
				id: "delete",
				label: "Delete…",
				icon: Trash2Icon,
				run: onDelete,
				disabled: false,
				destructive: true,
			},
		);
		return {
			deploy: deployNow,
			menu,
			busy: deploy.isPending || restart.isPending || stop.isPending,
		};
	}, [
		service,
		stopped,
		isProduction,
		confirm,
		confirmDeployNoCache,
		actions.deployNoCache.isPending,
		deploy,
		restart,
		stop,
		cancel,
		maintenance,
		show,
		onDelete,
	]);
}
