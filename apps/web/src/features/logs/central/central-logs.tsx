import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { RefreshCwIcon } from "lucide-react";
import { type ReactNode, useMemo, useState } from "react";
import { errorMessage, errorStatus } from "#/api/client-config";
import { getServiceCentralLogsOptions } from "#/api/gen/@tanstack/react-query.gen";
import type { Service } from "#/api/models";
import { Loading } from "#/components/common/loading";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { Spinner } from "#/components/ui/spinner";
import { resolveRange } from "../history/model";
import { RangePicker } from "../history/range-picker";
import { useHistoryRange } from "../history/url";
import { LogView } from "../log-view";
import { LogSearchShell, NoMatches } from "../search/log-search-shell";
import { useLogSearch } from "../search/use-log-search";
import {
	CENTRAL_LIMIT,
	centralErrorText,
	centralFooter,
	centralLines,
	centralState,
} from "./model";

/**
 * The service's "Central logs (7 days)": its lines from the team's built-in log
 * store, collected from every server it ran on, newest at the bottom. The store
 * is the control plane's own, so it answers while the service's server is
 * offline. While the team's central logs are off, the view says so in one line
 * with where to turn them on.
 */
export function CentralLogs({
	service,
	modeToggle,
	chips = null,
	process,
}: {
	service: Service;
	modeToggle: ReactNode;
	/** The process filter of a service with processes. */
	chips?: ReactNode;
	/** Services with processes: one process' lines. */
	process?: string | undefined;
}) {
	const [range, setRange] = useHistoryRange();
	// "Now" is pinned for a range that reaches it until Refresh asks again,
	// so the request key is stable and nothing refetches by itself.
	const signature = JSON.stringify(range);
	const [anchor, setAnchor] = useState({ signature, at: Date.now() });
	let at = anchor.at;
	if (anchor.signature !== signature) {
		const next = { signature, at: Date.now() };
		setAnchor(next);
		at = next.at;
	}
	const resolved = useMemo(() => resolveRange(range, at), [range, at]);
	const answer = useQuery({
		...getServiceCentralLogsOptions({
			path: { serviceId: service.id },
			query: {
				...(resolved.from ? { from: resolved.from } : {}),
				to: resolved.to,
				limit: CENTRAL_LIMIT,
				...(process ? { process } : {}),
			},
		}),
		retry: false,
		refetchOnWindowFocus: false,
	});
	const lines = useMemo(() => centralLines(answer.data), [answer.data]);
	const search = useLogSearch({ lines });
	const state = answer.data ? centralState(answer.data) : null;

	let empty: ReactNode = "No lines in this range.";
	if (search.total > 0 && search.settled.input.trim() !== "") {
		empty = <NoMatches query={search.settled.input} onClear={search.clear} />;
	}

	const refresh = () => {
		setAnchor({ signature, at: Date.now() });
		void answer.refetch();
	};

	const toolbar = (
		<>
			<div className="flex h-8 items-center">{modeToggle}</div>
			<RangePicker value={range} onChange={setRange} />
			<Button
				variant="secondary"
				size="sm"
				title="Ask the log store again"
				disabled={answer.isFetching}
				onClick={refresh}
			>
				{answer.isFetching ? <Spinner /> : <RefreshCwIcon aria-hidden />}
				Refresh
			</Button>
		</>
	);

	return (
		<section
			data-tab="logs"
			data-service-id={service.id}
			aria-label="Central logs"
			className="flex h-full min-h-[360px] flex-col gap-3 p-4"
		>
			{chips}
			{state?.kind === "off" ? (
				<>
					<ToggleRow>{modeToggle}</ToggleRow>
					<CentralOff hint={state.hint} />
				</>
			) : answer.isError ? (
				<>
					<ToggleRow>{modeToggle}</ToggleRow>
					<Alert variant="destructive">
						<AlertTitle>Central logs did not load</AlertTitle>
						<AlertDescription className="flex flex-col gap-2">
							<span>
								{centralErrorText(
									errorStatus(answer.error),
									errorMessage(answer.error),
								)}
							</span>
							<span>
								<Button variant="secondary" size="sm" onClick={refresh}>
									Try again
								</Button>
							</span>
						</AlertDescription>
					</Alert>
				</>
			) : answer.isPending ? (
				<>
					<ToggleRow>{modeToggle}</ToggleRow>
					<Loading label="Loading central logs" />
				</>
			) : (
				<>
					<LogSearchShell
						search={search}
						label="Search central logs"
						facetKeys={["level", "stream"]}
						className="min-h-0 flex-1"
						bodyClassName="flex-1"
						toolbar={toolbar}
					>
						<LogView
							className="surface-well absolute inset-0 rounded-card"
							lines={search.visible}
							terms={search.terms}
							showLevel
							showDate
							follow
							onLetGo={() => undefined}
							label={`${service.name} central logs`}
							empty={empty}
						/>
					</LogSearchShell>
					<footer className="text-ink-3 text-xs">
						{centralFooter(answer.data)}
						{state?.kind === "lines" && state.truncated
							? ` Showing the newest ${CENTRAL_LIMIT} lines of the range: narrow it to see older ones.`
							: ""}
					</footer>
				</>
			)}
		</section>
	);
}

/**
 * The mode toggle on a row of its own, kept where the search row puts it
 * (after a search-field-wide gap), so it does not jump when the view has no
 * search field to show.
 */
function ToggleRow({ children }: { children: ReactNode }) {
	return (
		<div className="flex flex-wrap items-start gap-x-3 gap-y-2">
			<div aria-hidden className="min-w-[220px] flex-1" />
			<div className="flex h-8 items-center">{children}</div>
		</div>
	);
}

/** Central logs are off: what that means and where to turn them on. */
function CentralOff({ hint }: { hint: string }) {
	return (
		<div
			role="status"
			className="surface-well flex flex-1 flex-col items-start justify-center gap-3 rounded-card p-6"
		>
			<p className="m-0 max-w-[60ch] text-ink-2 text-sm leading-relaxed">
				{hint}
			</p>
			<Button
				variant="secondary"
				size="sm"
				nativeButton={false}
				render={
					<Link
						to="/settings/$section"
						params={{ section: "log-forwarding" }}
					/>
				}
			>
				Open Log forwarding
			</Button>
		</div>
	);
}
