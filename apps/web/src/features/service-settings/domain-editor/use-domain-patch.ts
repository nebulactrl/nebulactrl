import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
	getService,
	setDomainAuthUsers,
	updateDomain,
} from "#/api/gen/sdk.gen";
import type { AuthUserBody, UpdateInputBody } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { toast } from "#/components/ui/toast";
import { isGenerationConflict } from "#/features/proxy/lib/errors";
import { invalidateRoutes } from "#/features/proxy/queries";

export interface DomainSave {
	domainId: string;
	/** The PATCH body; omitted when only the auth users change. */
	body?: UpdateInputBody;
	/**
	 * Basic-auth users to store, sent before (`usersFirst`) or after the PATCH.
	 * Enabling basic auth needs the users to exist first; disabling it has to
	 * clear the flag before the last user goes.
	 */
	users?: AuthUserBody[];
	usersFirst?: boolean;
}

/**
 * The one way a domain editor saves: an optional `PUT /auth-users` and an
 * optional `PATCH /domains/{id}` with `If-Match` set to the service
 * generation. A PUT moves the generation, so the PATCH reads the service
 * again first. Everything lands staged; nothing reaches the proxy until the
 * next deploy.
 */
export function useDomainSave(service: Pick<Service, "id" | "generation">) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: async (save: DomainSave) => {
			const putUsers = async () => {
				if (save.users) {
					await setDomainAuthUsers({
						path: { domainId: save.domainId },
						body: save.users,
					});
				}
			};
			const patch = async (generation: number) => {
				if (save.body && Object.keys(save.body).length > 0) {
					await updateDomain({
						path: { domainId: save.domainId },
						headers: { "If-Match": `"${generation}"` },
						body: save.body,
					});
				}
			};
			if (save.usersFirst) {
				await putUsers();
				const { data } = await getService({ path: { serviceId: service.id } });
				await patch(data.generation);
			} else {
				await patch(service.generation);
				if (save.users) {
					await putUsers();
				}
			}
		},
		onSuccess: async () => {
			await invalidateRoutes(queryClient);
			toast.success("Domain settings saved", "Staged until the next deploy.");
		},
		onError: (error) => {
			// A 412 was toasted and refetched by the global handler; the other
			// failures show inline under the group that saved.
			if (!isGenerationConflict(error)) {
				void invalidateRoutes(queryClient);
			}
		},
	});
}
