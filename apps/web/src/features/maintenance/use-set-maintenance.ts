import { useMutation, useQueryClient } from "@tanstack/react-query";
import { setServiceMaintenance } from "#/api/gen/sdk.gen";
import type { SetMaintenanceInputBody } from "#/api/gen/types.gen";
import { invalidateServices } from "#/api/invalidate";
import type { Service } from "#/api/models";
import { toast } from "#/components/ui/toast";

/**
 * Turns maintenance on or off. It is operational, like scaling: the proxy
 * switches at once, nothing is staged and no deployment runs.
 */
export function useSetMaintenance(service: Pick<Service, "id" | "name">) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (body: SetMaintenanceInputBody) => {
			await setServiceMaintenance({
				path: { serviceId: service.id },
				body,
				headers: { "Idempotency-Key": crypto.randomUUID() },
			});
			return body.enabled;
		},
		onSuccess: async (enabled) => {
			await invalidateServices(queryClient);
			if (enabled) {
				toast.success(
					`${service.name} is in maintenance`,
					"Visitors see the maintenance page (HTTP 503).",
				);
			} else {
				toast.success(
					`${service.name} is back`,
					"Maintenance is off and traffic reaches the service again.",
				);
			}
		},
	});
}
