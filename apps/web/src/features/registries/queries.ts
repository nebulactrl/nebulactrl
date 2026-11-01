import {
	type QueryClient,
	queryOptions,
	useQuery,
} from "@tanstack/react-query";
import { useIsAdmin } from "#/features/proxy/use-admin";
import { fetchImages, fetchRegistries } from "./api";
import type { ImageState } from "./model";

/** The built-in registry answers or not; the list is re-read on a timer. */
const REGISTRY_REFRESH_MS = 15_000;

const ROOT = ["registries"] as const;

export const registriesQuery = () =>
	queryOptions({
		queryKey: [...ROOT, "list"] as const,
		queryFn: ({ signal }) => fetchRegistries(signal),
		refetchInterval: REGISTRY_REFRESH_MS,
	});

export const registryImagesQuery = (registryId: string, state: ImageState) =>
	queryOptions({
		queryKey: [...ROOT, registryId, "images", state] as const,
		queryFn: ({ signal }) => fetchImages(registryId, state, signal),
	});

/** After any registry change: the list, the images and, through them, every select that names one. */
export function invalidateRegistries(queryClient: QueryClient): Promise<void> {
	return queryClient.invalidateQueries({ queryKey: ROOT });
}

/**
 * The registry list for a select, only for a caller who may read it: listing
 * registries needs the admin scope, and a request that is certain to be
 * refused would raise a 403 toast on every settings page that offers a choice.
 */
export function useRegistries() {
	const admin = useIsAdmin();
	return useQuery({ ...registriesQuery(), enabled: admin });
}
