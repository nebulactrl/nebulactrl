import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { errorMessage } from "#/api/client-config";
import { createRoute, deployService } from "#/api/gen/sdk.gen";
import type { CreateRouteInputBody } from "#/api/gen/types.gen";
import { type Environment, type Service, toService } from "#/api/models";
import { toast } from "#/components/ui/toast";
import { placeNode } from "#/features/canvas/use-layout-persistence";
import { openStagedReview } from "#/features/deploy-button";
import { routeCreate } from "../create-store";
import { invalidateRoutes } from "../queries";

/**
 * Creates a route service (`POST /environments/{id}/routes`), drops its node
 * where the user asked, and offers the deploy: a route is staged like any new
 * service, and nothing is published until it is deployed.
 */
export function useCreateRoute(
	environment: Pick<Environment, "id" | "name" | "isProduction">,
	position: { x: number; y: number } | undefined,
) {
	const queryClient = useQueryClient();
	// One key per opening: a retry after a lost response replays, not duplicates.
	const [idempotencyKey] = useState(() => crypto.randomUUID());

	const deployNow = async (service: Service) => {
		try {
			await deployService({
				path: { serviceId: service.id },
				headers: { "Idempotency-Key": crypto.randomUUID() },
			});
			toast.deployed(
				`Deploying ${service.name}`,
				"Follow it on its card or under Deployments.",
			);
			await invalidateRoutes(queryClient);
		} catch (failure) {
			toast.error(`Could not deploy ${service.name}`, errorMessage(failure));
		}
	};

	return useMutation({
		mutationFn: async (body: CreateRouteInputBody) => {
			const { data } = await createRoute({
				path: { environmentId: environment.id },
				body,
				headers: { "Idempotency-Key": idempotencyKey },
			});
			return toService(data);
		},
		onSuccess: async (service) => {
			routeCreate.close();
			await invalidateRoutes(queryClient);
			if (position) {
				try {
					await placeNode(service.id, position);
				} catch {
					toast.warning(
						`${service.name} was placed automatically`,
						"Its position could not be saved where you dropped it.",
					);
				}
			}
			toast.add({
				type: "success",
				title: `Created ${service.name}`,
				description: environment.isProduction
					? `Staged in ${environment.name}. Nothing is published until you review and deploy.`
					: "Nothing is published until it is deployed.",
				timeout: 9000,
				actionProps: {
					children: environment.isProduction ? "Review & deploy" : "Deploy now",
					onClick: () => {
						if (environment.isProduction) openStagedReview();
						else void deployNow(service);
					},
				},
			});
		},
	});
}
