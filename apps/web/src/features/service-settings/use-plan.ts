import { useQuery } from "@tanstack/react-query";
import { planDeployment } from "#/api/gen/sdk.gen";
import { type Service, toPlan } from "#/api/models";

/**
 * The deployment plan of a service: what the next deploy will do, from the
 * saved configuration. Shared by the Releases fact line (strategy) and the
 * Build fact line (placement), which read the same query.
 */
export function usePlan(service: Service) {
	return useQuery({
		queryKey: ["plan", service.id, service.generation, service.serverId],
		queryFn: async ({ signal }) => {
			const { data } = await planDeployment({
				path: { serviceId: service.id },
				signal,
			});
			return toPlan(data);
		},
		refetchInterval: false,
		retry: false,
	});
}
