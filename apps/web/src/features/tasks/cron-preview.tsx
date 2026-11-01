import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { cn } from "cn";
import type { ReactNode } from "react";
import { errorMessage } from "#/api/client-config";
import { previewTaskSchedule } from "#/api/gen/sdk.gen";
import { FieldError } from "#/components/ui/field";
import { useDebounced } from "#/features/templates/use-debounced";
import { formatRunTimes } from "./model";

const DEBOUNCE_MS = 300;
const PREVIEW_COUNT = 5;

/**
 * What a schedule means: the control plane's English reading of it and the
 * next five runs, each in the schedule's own time zone, in UTC and, when it
 * is a third zone, in the reader's. Asks the API, so the preview is exactly
 * what the scheduler will do (daylight-saving changes included); typing waits
 * 300ms before a request goes out.
 */
export function CronPreview({
	schedule,
	timezone,
}: {
	schedule: string;
	/** IANA zone the schedule is evaluated in; "" is UTC. */
	timezone: string;
}) {
	const typed = schedule.trim();
	const settled = useDebounced(typed, DEBOUNCE_MS);
	const zone = useDebounced(timezone.trim(), DEBOUNCE_MS);
	const preview = useQuery({
		queryKey: ["task-schedule-preview", settled, zone],
		queryFn: async () => {
			const { data } = await previewTaskSchedule({
				body: { schedule: settled, timezone: zone, count: PREVIEW_COUNT },
			});
			return data;
		},
		enabled: settled !== "",
		staleTime: 60_000,
		placeholderData: keepPreviousData,
	});
	const stale =
		typed !== settled ||
		timezone.trim() !== zone ||
		(preview.isFetching && !preview.isError);
	const data = preview.data;

	let body: ReactNode;
	if (typed === "") {
		body = (
			<p className="text-ink-3 text-xs">
				Enter a schedule to see when it runs.
			</p>
		);
	} else if (preview.isError && !data) {
		body = <FieldError>{errorMessage(preview.error)}</FieldError>;
	} else if (!data) {
		body = <p className="text-ink-3 text-xs">Checking the schedule…</p>;
	} else if (!data.valid) {
		body = (
			<FieldError>{data.error ?? "This is not a valid schedule."}</FieldError>
		);
	} else {
		const runs = data.nextRuns ?? [];
		body = (
			<>
				<p className="font-medium text-ink-1 text-xs">
					{data.description ?? "Runs on this schedule"}
				</p>
				{runs.length > 0 ? (
					<>
						<p className="text-ink-3 text-xs">Next runs:</p>
						<ol className="flex flex-col gap-0.5 font-mono text-ink-2 text-xs tabular-nums">
							{runs.map((iso) => {
								const when = formatRunTimes(iso, data.timezone ?? "UTC");
								const zoneName = data.timezone ?? "UTC";
								return (
									<li key={iso}>
										{when.zoned} <span className="text-ink-4">{zoneName}</span>
										{zoneName === "UTC" ? null : (
											<>
												<span className="text-ink-4"> · </span>
												{when.utc}
											</>
										)}
										{when.local ? (
											<>
												<span className="text-ink-4"> · </span>
												{when.local}{" "}
												<span className="text-ink-4">your time</span>
											</>
										) : null}
									</li>
								);
							})}
						</ol>
					</>
				) : null}
			</>
		);
	}

	return (
		<div
			aria-live="polite"
			data-slot="cron-preview"
			className={cn(
				"surface-well flex min-h-[112px] flex-col gap-1.5 rounded-card px-3 py-2.5",
				stale && data ? "opacity-70" : null,
			)}
		>
			{body}
		</div>
	);
}
