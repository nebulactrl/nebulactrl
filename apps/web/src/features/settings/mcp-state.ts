import { getSystemOptions } from "#/api/gen/@tanstack/react-query.gen";

/**
 * Whether MCP is on, whether the environment decides, and its URL (from
 * GET /system). It changes only through the Admin switch, which invalidates it.
 */
export const mcpSystemQuery = () => ({
	...getSystemOptions(),
	refetchInterval: false as const,
	staleTime: 60_000,
});
