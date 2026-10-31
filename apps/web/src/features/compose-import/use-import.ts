import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useRef } from "react";
import { errorMessage, isApiError } from "#/api/client-config";
import { getCanvas, importCompose } from "#/api/gen/sdk.gen";
import type {
	ComposeImportRequest,
	ComposeImportResult,
} from "#/api/gen/types.gen";
import { canvasQuery } from "#/api/hooks";
import { invalidateServices } from "#/api/invalidate";
import { type Environment, toCanvasDocument } from "#/api/models";
import { toast } from "#/components/ui/toast";
import { landOnCanvas } from "#/features/canvas/canvas-landing";
import {
	type ImportFailure,
	importFailure,
	stagedMessage,
	stagedStackMessage,
} from "./lib";

/**
 * One key per distinct request: a retry after a lost response replays the
 * import, while a changed server, choice or source is a new request.
 */
function useIdempotencyKey() {
	const last = useRef({ fingerprint: "", key: "" });
	return (request: ComposeImportRequest): string => {
		const fingerprint = JSON.stringify(request);
		if (last.current.fingerprint !== fingerprint) {
			last.current = { fingerprint, key: crypto.randomUUID() };
		}
		return last.current.key;
	};
}

/**
 * Stages (or stages and deploys) the reviewed Compose project in the
 * environment on screen. On success the canvas takes the layout the server
 * saved, with the new group, the service lists refetch, and a toast says what
 * was staged. Warnings from the server stay in a toast until dismissed.
 */
export function useComposeImport({
	environment,
	projectId,
	onImported,
}: {
	environment: Pick<Environment, "id">;
	projectId: string;
	onImported(): void;
}) {
	const queryClient = useQueryClient();
	const keyFor = useIdempotencyKey();

	const land = async (result: ComposeImportResult) => {
		const serviceIds = (result.services ?? []).map((service) => service.id);
		try {
			// The server wrote the group into the saved layout; hand that version
			// to the canvas before the new cards appear, so it does not lay them
			// out again on top of it.
			const { data } = await getCanvas({
				path: { projectId, environmentId: environment.id },
			});
			const document = toCanvasDocument(data);
			queryClient.setQueryData(
				canvasQuery(projectId, environment.id).queryKey,
				document,
			);
			// A stack is one node: there is no group to bring into view.
			if (result.groupId !== "") {
				landOnCanvas({ document, groupId: result.groupId, serviceIds });
			}
		} catch (error) {
			toast.warning(
				"The new group could not be placed",
				`${errorMessage(error)} The services are on the canvas; arrange them there.`,
			);
		}
		await invalidateServices(queryClient);
	};

	const stage = useMutation<ComposeImportResult, unknown, ComposeImportRequest>(
		{
			mutationFn: async (request) => {
				const { data } = await importCompose({
					path: { environmentId: environment.id },
					body: request,
					headers: { "Idempotency-Key": keyFor(request) },
				});
				return data;
			},
			onSuccess: async (result, request) => {
				onImported();
				const count = (result.services ?? []).length;
				const asStack = request.mode === "stack";
				if (request.deploy) {
					toast.deployed(
						asStack
							? `Deploying ${result.name}`
							: `Deploying ${count} ${count === 1 ? "service" : "services"}`,
						asStack
							? "Watch its card for progress."
							: "Watch each card for progress.",
					);
				} else {
					toast.success(
						asStack ? stagedStackMessage(result.name) : stagedMessage(count),
						`${result.name} is on the canvas. Nothing runs until it is deployed.`,
					);
				}
				const warnings = result.warnings ?? [];
				if (warnings.length > 0) {
					toast.add({
						type: "warning",
						title: "Notes about this import",
						description: warnings.join(" "),
						timeout: 0,
						dismissible: true,
					});
				}
				await land(result);
			},
		},
	);

	const failure: ImportFailure | null = stage.isError
		? importFailure({
				status: isApiError(stage.error) ? stage.error.status : undefined,
				code: isApiError(stage.error) ? stage.error.code : undefined,
				message: errorMessage(stage.error),
				details: isApiError(stage.error) ? stage.error.details : undefined,
			})
		: null;

	return { stage, failure };
}
