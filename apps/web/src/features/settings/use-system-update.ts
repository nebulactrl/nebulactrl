import { queryOptions, useQuery } from "@tanstack/react-query";
import { useEffect } from "react";
import { getSystemUpdateOptions } from "#/api/gen/@tanstack/react-query.gen";
import { toast } from "#/components/ui/toast";
import {
	bareVersion,
	chipLabel,
	isToastDue,
	MARKER_KEY,
	parseMarker,
	pollInterval,
	type ReloadMarker,
	shouldReload,
	updatedToastTitle,
} from "./update-model";

/**
 * `GET /system/update`. Read every few seconds while an update runs, rarely
 * otherwise. No retries: the interval is the only cadence, and a failed read
 * during a restart keeps the last answer on screen. The interval runs in a
 * hidden tab too, so an update finished while the tab was in the background
 * is noticed on return.
 */
export const systemUpdateQuery = () =>
	queryOptions({
		...getSystemUpdateOptions(),
		staleTime: 60_000,
		retry: false,
		refetchInterval: (query) => pollInterval(query.state.data, Date.now()),
		refetchIntervalInBackground: true,
	});

function readMarker(): ReloadMarker | null {
	try {
		return parseMarker(window.sessionStorage.getItem(MARKER_KEY));
	} catch {
		return null;
	}
}

function writeMarker(marker: ReloadMarker): void {
	try {
		window.sessionStorage.setItem(MARKER_KEY, JSON.stringify(marker));
	} catch {
		// Storage is blocked: the page still reloads once, it just cannot say "Updated".
	}
}

/** The control-plane version this page first heard of; it resets with every page load. */
let pageVersion: string | undefined;

/**
 * The sidebar's view of updates: the chip text for a newer release, and the
 * hand-over to the new UI once the control plane reports a different version
 * than the one this page started on. It reloads the page once (the marker
 * keeps that from looping) and, after the reload, raises one "Updated to
 * v0.1.2" toast. Mounted once, where the shell lives, so the reload happens
 * on whatever page the person is on.
 */
export function useUpdateNotice(): { chip: string | null } {
	const query = useQuery(systemUpdateQuery());
	const current = query.data?.current;

	useEffect(() => {
		if (current === undefined) return;
		pageVersion ??= current;
		const marker = readMarker();
		if (isToastDue(marker, current)) {
			toast.success(updatedToastTitle(current));
			writeMarker({ version: bareVersion(current), toasted: true });
			return;
		}
		if (shouldReload({ pageVersion, current, marker })) {
			writeMarker({ version: bareVersion(current), toasted: false });
			window.location.reload();
		}
	}, [current]);

	return { chip: chipLabel(query.data) };
}
