import { useQuery } from "@tanstack/react-query";
import { linksQuery, serversQuery, servicesQuery } from "#/api/hooks";

export function useServices(environmentId: string | undefined) {
	return useQuery({
		...servicesQuery(environmentId ?? ""),
		enabled: environmentId !== undefined,
	});
}

export function useLinks(environmentId: string | undefined) {
	return useQuery({
		...linksQuery(environmentId ?? ""),
		enabled: environmentId !== undefined,
		// A missing link list never blocks the canvas.
		retry: false,
	});
}

export function useServers() {
	return useQuery({ ...serversQuery(), retry: false });
}
