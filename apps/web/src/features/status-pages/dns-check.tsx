import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { RefreshCwIcon } from "lucide-react";
import { useState } from "react";
import { errorCode, errorMessage } from "#/api/client-config";
import { CopyButton } from "#/components/common/copy-button";
import { NewNameNote } from "#/components/common/new-name-note";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { dnsNotice, isNewName, recordLine } from "./model";
import { statusPageDnsQuery } from "./queries";

/** The records to create by hand, with copy buttons. */
function RecordsList({
	records,
}: {
	records: readonly { name: string; type: string; value: string }[];
}) {
	return (
		<div className="flex flex-col gap-1.5">
			<div className="flex items-center justify-between gap-2">
				<h4 className="m-0 type-label">Create these records</h4>
				{records.length > 0 ? (
					<CopyButton
						variant="ghost"
						size="sm"
						value={records.map(recordLine).join("\n")}
					>
						Copy all
					</CopyButton>
				) : null}
			</div>
			{records.length === 0 ? (
				<p className="surface-well m-0 rounded-card p-3 text-ink-3 text-xs">
					The control plane does not know its public address yet, so it cannot
					tell you which records to create. Set the address of the control-plane
					server, then check again.
				</p>
			) : (
				<ul className="surface-well m-0 flex list-none flex-col divide-y divide-line-faint rounded-card p-0">
					{records.map((record) => (
						<li
							key={`${record.type}-${record.value}`}
							className="flex items-center gap-3 px-3 py-2"
						>
							<span className="w-12 shrink-0 font-mono text-2xs text-ink-3">
								{record.type}
							</span>
							<code className="type-mono min-w-0 flex-1 truncate text-ink-1">
								{record.name}
							</code>
							<code className="type-mono shrink-0 text-ink-1">
								{record.value}
							</code>
							<CopyButton
								variant="ghost"
								size="icon-sm"
								label={`Copy the ${record.type} record ${record.value}`}
								value={record.value}
							/>
						</li>
					))}
				</ul>
			)}
		</div>
	);
}

/**
 * Where the hostname has to point, what it points at now, and a Recheck. The
 * records are the control-plane server's public addresses: the status page is
 * served from there (and only the page; the dashboard is never reachable
 * through this name). DNS changes take a while, so nothing polls: Recheck asks
 * again, past the server's one-minute cache of lookups.
 */
export function DnsCheck({
	pageId,
	managed = false,
}: {
	pageId: string;
	/** True inside the Cloudflare panel: NebulaCtrl creates the records, so none are listed to create by hand. */
	managed?: boolean;
}) {
	// Inside the Cloudflare panel the check follows a change to the record, so it never reads cached lookups.
	const [fresh, setFresh] = useState(managed);
	const check = useQuery({
		...statusPageDnsQuery(pageId, true, fresh),
		placeholderData: keepPreviousData,
	});
	const recheck = () => {
		// The first Recheck switches to the uncached lookup; later ones refetch it.
		if (fresh) void check.refetch();
		else setFresh(true);
	};
	const data = check.data;

	if (check.isPending) {
		return (
			<Skeleton
				className="h-28 rounded-card"
				role="status"
				aria-label="Checking DNS"
			/>
		);
	}

	if (!data) {
		return (
			<Alert variant="destructive">
				<AlertTitle>DNS could not be checked</AlertTitle>
				<AlertDescription>
					{errorCode(check.error) === "no_hostname"
						? "Save a hostname first."
						: errorMessage(check.error)}
					<Button
						type="button"
						variant="secondary"
						size="sm"
						className="mt-2 w-fit"
						onClick={() => void check.refetch()}
					>
						Try again
					</Button>
				</AlertDescription>
			</Alert>
		);
	}

	const records = data.records ?? [];
	const resolved = data.resolved ?? [];
	const notice = dnsNotice(
		data,
		managed,
		isNewName(data.nameChangedAt, Date.now()),
	);
	return (
		<div className="flex flex-col gap-3">
			<Alert variant={notice.variant}>
				<AlertTitle>{notice.title}</AlertTitle>
				<AlertDescription>
					{notice.summary}
					{notice.showMessage && data.message ? (
						<span className="block">{data.message}</span>
					) : null}
				</AlertDescription>
			</Alert>
			{notice.headsUp ? <NewNameNote /> : null}

			{managed ? null : <RecordsList records={records} />}

			<div className="flex flex-wrap items-center justify-between gap-2">
				<p className="m-0 text-ink-3 text-xs">
					{resolved.length > 0 ? (
						<>
							Resolves to now:{" "}
							<span className="font-mono text-ink-2">
								{resolved.join(", ")}
							</span>
						</>
					) : (
						"Resolves to nothing right now."
					)}
				</p>
				<Button
					type="button"
					variant="secondary"
					size="sm"
					disabled={check.isFetching}
					onClick={recheck}
				>
					{check.isFetching ? <Spinner /> : <RefreshCwIcon />}
					{check.isFetching ? "Checking…" : "Recheck"}
				</Button>
			</div>
		</div>
	);
}
