import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useRef } from "react";
import { errorMessage, errorStatus } from "#/api/client-config";
import { getCanvas, installTemplate } from "#/api/gen/sdk.gen";
import type { TemplateInstallResult } from "#/api/gen/types.gen";
import { canvasQuery } from "#/api/hooks";
import { invalidateOperations, invalidateServices } from "#/api/invalidate";
import { type Environment, toCanvasDocument } from "#/api/models";
import { toast } from "#/components/ui/toast";
import { landOnCanvas } from "#/features/canvas/canvas-landing";
import { openStagedReview } from "#/features/deploy-button";
import { useStagedActions } from "#/features/staged/use-staged";
import { type InstallFailure, installedTitle, installFailure } from "./lib";

export interface InstallArgs {
	serverId: string;
	inputs: Record<string, string>;
	deploy: boolean;
}

/**
 * One key per distinct request: a retry after a lost response replays the
 * install, while a changed server, input or choice is a new request.
 */
function useIdempotencyKey() {
	const last = useRef({ fingerprint: "", key: "" });
	return (args: InstallArgs): string => {
		const fingerprint = JSON.stringify([
			args.serverId,
			args.deploy,
			Object.entries(args.inputs).sort(([a], [b]) => a.localeCompare(b)),
		]);
		if (last.current.fingerprint !== fingerprint) {
			last.current = { fingerprint, key: crypto.randomUUID() };
		}
		return last.current.key;
	};
}

/**
 * Installs a template into the environment on screen. On success the canvas
 * takes the layout the server saved (with the new group), the service and
 * stack lists refetch, and a toast says what was added.
 */
export function useInstallTemplate({
	environment,
	projectId,
	templateId,
	templateName,
	onInstalled,
}: {
	environment: Pick<Environment, "id" | "name" | "isProduction">;
	projectId: string;
	templateId: string;
	templateName: string;
	onInstalled(): void;
}) {
	const queryClient = useQueryClient();
	const keyFor = useIdempotencyKey();
	const stagedActions = useStagedActions(environment.id, undefined);

	const land = async (result: TemplateInstallResult) => {
		const serviceIds = (result.services ?? []).map((service) => service.id);
		try {
			// The server wrote the group into the saved layout; hand that
			// version to the canvas before the new cards appear, so it does
			// not lay them out again on top of it.
			const { data } = await getCanvas({
				path: { projectId, environmentId: environment.id },
			});
			const document = toCanvasDocument(data);
			queryClient.setQueryData(
				canvasQuery(projectId, environment.id).queryKey,
				document,
			);
			landOnCanvas({
				document,
				groupId: result.instance.groupId,
				serviceIds,
			});
		} catch (error) {
			toast.warning(
				"The new group could not be placed",
				`${errorMessage(error)} The services are on the canvas; arrange them there.`,
			);
		}
		await Promise.all([
			invalidateServices(queryClient),
			invalidateOperations(queryClient, ["listTemplateInstances"]),
		]);
	};

	const deployStack = (serviceIds: readonly string[]) => {
		if (environment.isProduction) openStagedReview();
		else void stagedActions.deploy.mutateAsync({ serviceIds }).catch(() => {});
	};

	const install = useMutation<TemplateInstallResult, unknown, InstallArgs>({
		mutationFn: async (args) => {
			const { data } = await installTemplate({
				path: { environmentId: environment.id, templateId },
				body: {
					serverId: args.serverId,
					inputs: args.inputs,
					...(args.deploy ? { deploy: true } : {}),
				},
				headers: { "Idempotency-Key": keyFor(args) },
			});
			return data;
		},
		onSuccess: async (result, args) => {
			onInstalled();
			const count = (result.services ?? []).length;
			const ids = (result.services ?? []).map((service) => service.id);
			const warnings = result.warnings ?? [];
			if (args.deploy) {
				toast.deployed(
					installedTitle(count, true),
					warnings.length > 0
						? warnings.join(" ")
						: "Watch each card for progress.",
				);
			} else {
				toast.add({
					type: "success",
					title: installedTitle(count, false),
					description:
						warnings.length > 0
							? warnings.join(" ")
							: `${templateName} is on the canvas. Nothing runs until it is deployed.`,
					timeout: 9000,
					actionProps: {
						children: environment.isProduction
							? "Review & deploy"
							: "Deploy stack",
						onClick: () => deployStack(ids),
					},
				});
			}
			await land(result);
		},
	});

	const failure: InstallFailure | null = install.isError
		? installFailure(
				{
					status: errorStatus(install.error),
					message: errorMessage(install.error),
				},
				templateName,
			)
		: null;

	return { install, failure };
}
