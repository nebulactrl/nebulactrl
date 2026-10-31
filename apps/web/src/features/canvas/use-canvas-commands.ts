import { useQueryClient } from "@tanstack/react-query";
import {
	ArrowRightLeftIcon,
	ArrowUpRightIcon,
	BoxesIcon,
	BoxIcon,
	FileCodeIcon,
	FolderOpenIcon,
	LayersIcon,
	PlusIcon,
	RefreshCwIcon,
	RocketIcon,
	SlidersHorizontalIcon,
	TerminalIcon,
	TerminalSquareIcon,
} from "lucide-react";
import { useMemo } from "react";
import { errorMessage } from "#/api/client-config";
import { invalidateServices } from "#/api/invalidate";
import type { Service } from "#/api/models";
import {
	type PaletteCommand,
	useRegisterCommands,
} from "#/components/shell/palette-registry";
import { toast } from "#/components/ui/toast";
import { composeImportActions } from "#/features/compose-import/store";
import { coolifyImportActions } from "#/features/coolify-import/store";
import { openStagedReview } from "#/features/deploy-button";
import {
	startDeploy,
	startRestart,
} from "#/features/deployments/quick-actions";
import { categoryCommands } from "#/features/service-settings/settings-nav-model";
import { createStackActions } from "#/features/stacks/store";
import { openRunCommand } from "#/features/tasks";
import { templatesActions } from "#/features/templates/store";
import { openFiles, openShell } from "#/features/terminal";
import { openTargets } from "#/lib/service-urls";
import type { InspectorTab } from "./inspector/tab-ids";
import { deployLabel } from "./lib/deploy-label";

/**
 * The canvas's ⌘K entries: open a service's inspector or its site, deploy or restart it,
 * and create a service. Registered while the canvas is mounted, so they come
 * and go with the environment on screen. Deploy follows the dock: in
 * production it opens the review sheet (the guarded key is the one
 * confirmation); elsewhere it deploys at once.
 */
export function useCanvasCommands({
	services,
	isProduction,
	onInspect,
	onCreate,
}: {
	services: readonly Service[];
	isProduction: boolean;
	onInspect(serviceId: string, tab?: InspectorTab, section?: string): void;
	onCreate(): void;
}) {
	const queryClient = useQueryClient();
	const commands = useMemo<PaletteCommand[]>(() => {
		const refresh = () => void invalidateServices(queryClient);
		const deploy = async (service: Service) => {
			// Production's one confirmation is the guarded key in the review sheet.
			if (isProduction) {
				openStagedReview({ serviceId: service.id });
				return;
			}
			try {
				await startDeploy(service.id);
				toast.deployed(
					`Deploying ${service.name}`,
					"Follow it under Deployments.",
				);
				refresh();
			} catch (error) {
				toast.error(`Could not deploy ${service.name}`, errorMessage(error));
			}
		};
		const entries: PaletteCommand[] = [
			{
				id: "canvas:create-service",
				label: "Create service",
				group: "Action",
				hint: "git, image or database",
				icon: PlusIcon,
				keywords: ["new", "add", "deploy", "database"],
				run: onCreate,
			},
			{
				id: "canvas:new-from-template",
				label: "New from template…",
				group: "Action",
				hint: "one-click apps",
				icon: LayersIcon,
				keywords: ["template", "templates", "app", "stack", "install", "game"],
				run: () => templatesActions.open(),
			},
			{
				id: "canvas:import-docker-compose",
				label: "Import Docker Compose…",
				group: "Action",
				hint: "paste or upload a compose file",
				icon: FileCodeIcon,
				keywords: [
					"compose",
					"docker-compose",
					"yaml",
					"yml",
					"import",
					"stack",
				],
				run: () => composeImportActions.open(),
			},
			{
				id: "canvas:import-coolify",
				label: "Import from Coolify…",
				group: "Action",
				hint: "projects, apps and databases",
				icon: ArrowRightLeftIcon,
				keywords: ["coolify", "import", "migrate", "migration", "move", "paas"],
				run: () => coolifyImportActions.open(),
			},
			{
				id: "canvas:new-compose-stack",
				label: "New Compose stack…",
				group: "Action",
				hint: "run a compose file as one service",
				icon: BoxesIcon,
				keywords: ["compose", "docker-compose", "yaml", "yml", "stack"],
				run: () => createStackActions.open(),
			},
		];
		for (const service of services) {
			entries.push(
				{
					id: `service:open:${service.id}`,
					label: service.name,
					group: "Service",
					hint: service.server?.name ?? "no server",
					icon: BoxIcon,
					keywords: ["open", "inspect", service.slug],
					run: () => onInspect(service.id),
				},
				{
					id: `service:deploy:${service.id}`,
					label: `${deployLabel(service)} ${service.name}`,
					group: "Action",
					hint: isProduction
						? "review, then confirm"
						: service.hasStagedChanges
							? "with its staged changes"
							: "from the current configuration",
					icon: RocketIcon,
					keywords: ["deploy", "redeploy", "release", service.slug],
					run: () => void deploy(service),
				},
				{
					id: `service:restart:${service.id}`,
					label: `Restart ${service.name}`,
					group: "Action",
					hint: "same release, new container process",
					icon: RefreshCwIcon,
					keywords: ["reboot", service.slug],
					run: () => {
						void startRestart(service.id).then(
							() => {
								toast.success(
									`Restarting ${service.name}`,
									"The command was sent to its server.",
								);
								refresh();
							},
							(error: unknown) =>
								toast.error(
									`Could not restart ${service.name}`,
									errorMessage(error),
								),
						);
					},
				},
			);
			// Every settings page of the service, found by what is inside it.
			// Only once the reader types: eight rows per service would bury the rest.
			for (const command of categoryCommands({
				kind: service.kind,
				sourceType: service.sourceType,
				oneshot: service.config.oneshot === true,
			})) {
				entries.push({
					id: `service:settings:${service.id}:${command.category}`,
					label: `Open ${service.name} settings: ${command.label}`,
					group: "Settings",
					icon: SlidersHorizontalIcon,
					keywords: [
						"settings",
						"configure",
						service.slug,
						...command.keywords,
					],
					searchOnly: true,
					run: () => onInspect(service.id, "settings", command.category),
				});
			}
			// "Open <host>": the service's first address is offered at once, the
			// rest once the reader types; a tailnet-only one says so.
			openTargets(service).forEach((target, index) => {
				entries.push({
					id: `service:open-site:${service.id}:${target.url}`,
					label: `Open ${target.host}`,
					group: "Action",
					hint: target.tailnetOnly
						? `${service.name}, tailnet only`
						: service.name,
					icon: ArrowUpRightIcon,
					keywords: ["visit", "browse", "site", "website", "url", service.slug],
					searchOnly: index > 0,
					run: () => window.open(target.url, "_blank", "noopener,noreferrer"),
				});
			});
			if (service.currentDeploymentId !== null && service.kind !== "route") {
				entries.push({
					id: `service:run:${service.id}`,
					label: `Run command in ${service.name}…`,
					group: "Action",
					hint: "one-off, in the service's image",
					icon: TerminalIcon,
					keywords: ["exec", "shell", "task", "migrate", service.slug],
					run: () => openRunCommand(service.id),
				});
				entries.push(
					{
						id: `service:shell:${service.id}`,
						label: `Open shell in ${service.name}…`,
						group: "Action",
						hint: "interactive terminal",
						icon: TerminalSquareIcon,
						keywords: [
							"terminal",
							"exec",
							"bash",
							"sh",
							"console",
							service.slug,
						],
						run: () => openShell(service.id),
					},
					{
						id: `service:files:${service.id}`,
						label: `Browse files of ${service.name}…`,
						group: "Action",
						hint: "inside the container",
						icon: FolderOpenIcon,
						keywords: ["files", "browser", "upload", "download", service.slug],
						run: () => openFiles(service.id),
					},
				);
			}
		}
		return entries;
	}, [services, isProduction, onInspect, onCreate, queryClient]);

	useRegisterCommands("canvas.services", commands);
}
