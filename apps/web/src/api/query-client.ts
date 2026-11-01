import { QueryClient } from "@tanstack/react-query";
import { ApiError } from "./client-config";

/** Every query polls at this interval; SSE invalidation makes it a fallback. */
const POLL_FALLBACK_MS = 15_000;

export const queryClient = new QueryClient({
	defaultOptions: {
		queries: {
			staleTime: 5_000,
			refetchInterval: POLL_FALLBACK_MS,
			// Hidden tabs do not poll; the SSE feed catches them up on return.
			refetchIntervalInBackground: false,
			refetchOnWindowFocus: false,
			// A 4xx will not change by asking again; 5xx and network errors may.
			retry: (failureCount, error) => {
				if (
					error instanceof ApiError &&
					error.status >= 400 &&
					error.status < 500
				) {
					return false;
				}
				return failureCount < 1;
			},
		},
	},
});
