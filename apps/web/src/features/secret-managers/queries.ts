import { type QueryClient, queryOptions } from "@tanstack/react-query";
import { listSecretManagersOptions } from "#/api/gen/@tanstack/react-query.gen";
import { invalidateOperations } from "#/api/invalidate";

/** The team's secret manager integrations (names, kinds, last test; never credentials). */
export const secretManagersQuery = () => {
	const options = listSecretManagersOptions();
	return queryOptions({
		...options,
		select: (list) => list.items ?? [],
	});
};

/** Everything that shows integrations; a variable that references one also changes. */
const SECRET_MANAGER_OPERATIONS = ["listSecretManagers"] as const;

export function invalidateSecretManagers(
	queryClient: QueryClient,
): Promise<void> {
	return invalidateOperations(queryClient, SECRET_MANAGER_OPERATIONS);
}
