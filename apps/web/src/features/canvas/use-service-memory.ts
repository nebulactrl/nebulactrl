import { useQuery } from "@tanstack/react-query";
import { useCallback } from "react";
import { getServiceMetricsOptions } from "#/api/gen/@tanstack/react-query.gen";
import type { MetricsOutputBody } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";

const MIB = 1024 * 1024;
const REFRESH_MS = 15_000;

/** What the card's memory bar draws: the latest sample against the limit (0: no limit). */
export interface CardMemory {
	usedBytes: number;
	limitBytes: number;
}

/**
 * The newest memory sample of a running service, for the card's thin bar.
 * Null until there is data, and always for a service that is not running:
 * the card shows what exists, never a placeholder.
 */
export function useServiceMemory(service: Service): CardMemory | null {
	const running =
		service.currentDeploymentId !== null &&
		service.desiredState === "running" &&
		service.observed?.state === "running";
	const configured = (service.config.memoryMb ?? 0) * MIB;
	const select = useCallback(
		(body: MetricsOutputBody): CardMemory | null => {
			const last = body.points?.at(-1);
			if (!last || last.replicaCount <= 0) return null;
			return {
				usedBytes: last.memBytes,
				limitBytes: last.memLimitBytes > 0 ? last.memLimitBytes : configured,
			};
		},
		[configured],
	);
	const query = useQuery({
		...getServiceMetricsOptions({
			path: { serviceId: service.id },
			query: { range: "5m" },
		}),
		enabled: running,
		refetchInterval: REFRESH_MS,
		staleTime: REFRESH_MS / 2,
		retry: false,
		select,
	});
	return running ? (query.data ?? null) : null;
}
