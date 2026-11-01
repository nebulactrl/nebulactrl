import { queryOptions, useQuery } from "@tanstack/react-query";
import { useMemo } from "react";
import { getDockerOptionsPolicyOptions } from "#/api/gen/@tanstack/react-query.gen";
import { type DockerPolicy, toDockerPolicy } from "./docker-options";

/**
 * What the Docker options accept: the allowed capabilities, sysctls, host
 * parameters, GPU capabilities, the reserved label prefixes and the stop
 * signals. It is the same for everyone and changes only with a release, so it
 * stays fresh for a while.
 */
export const dockerPolicyQuery = () =>
	queryOptions({
		...getDockerOptionsPolicyOptions(),
		staleTime: 300_000,
		refetchInterval: false,
	});

/** The policy as the form's rules read it. */
export function useDockerPolicy(): {
	policy: DockerPolicy | undefined;
	pending: boolean;
	failed: boolean;
} {
	const query = useQuery(dockerPolicyQuery());
	const policy = useMemo(
		() => (query.data ? toDockerPolicy(query.data) : undefined),
		[query.data],
	);
	return {
		policy,
		pending: query.isPending,
		failed: query.isError,
	};
}
