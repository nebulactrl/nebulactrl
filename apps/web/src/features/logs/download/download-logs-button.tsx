import { useMutation } from "@tanstack/react-query";
import { DownloadIcon } from "lucide-react";
import { client } from "#/api/gen/client.gen";
import { Button } from "#/components/ui/button";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuTrigger,
} from "#/components/ui/dropdown-menu";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import {
	type DownloadOptions,
	downloadQuery,
	filenameFromDisposition,
	LOG_FILE_FORMATS,
	type LogFileFormat,
} from "./model";

export type DownloadTarget =
	| { kind: "service"; id: string }
	| { kind: "deployment"; id: string };

async function problemMessage(res: Response): Promise<string> {
	try {
		const body = (await res.json()) as { detail?: string; title?: string };
		return body.detail || body.title || `HTTP ${res.status}`;
	} catch {
		return `HTTP ${res.status}`;
	}
}

/** Fetches the log file (same-origin session cookie) and hands it to the browser. */
async function downloadLogFile(
	target: DownloadTarget,
	options: DownloadOptions,
): Promise<void> {
	const url = client.buildUrl({
		url:
			target.kind === "service"
				? "/services/{serviceId}/logs/download"
				: "/deployments/{deploymentId}/logs/download",
		path:
			target.kind === "service"
				? { serviceId: target.id }
				: { deploymentId: target.id },
		query: downloadQuery(options),
	});
	const res = await fetch(url, { credentials: "include" });
	if (!res.ok) throw new Error(await problemMessage(res));
	const blob = await res.blob();
	const name = filenameFromDisposition(
		res.headers.get("Content-Disposition"),
		`${target.kind}-logs.${options.format}`,
	);
	const href = URL.createObjectURL(blob);
	const link = document.createElement("a");
	link.href = href;
	link.download = name;
	document.body.append(link);
	link.click();
	link.remove();
	URL.revokeObjectURL(href);
}

/**
 * "Download" menu of a log view: the stored log as a text or NDJSON file, with
 * the time range and search on screen (`range`/`query` are read when the item
 * is clicked). Secret values are masked by the control plane.
 */
export function DownloadLogsButton({
	target,
	range,
	query,
	component,
	process,
	label = "Download",
}: {
	target: DownloadTarget;
	/** Called at click time; undefined = everything the server keeps. */
	range?: () => { from?: string | undefined; to?: string | undefined };
	query?: string;
	component?: string | undefined;
	process?: string | undefined;
	label?: string;
}) {
	const download = useMutation({
		mutationFn: (format: LogFileFormat) =>
			downloadLogFile(target, {
				format,
				...range?.(),
				q: query,
				component,
				process,
			}),
		onError: (error) =>
			toast.error(
				"Could not download the log",
				error instanceof Error ? error.message : undefined,
			),
	});
	return (
		<DropdownMenu>
			<DropdownMenuTrigger
				render={
					<Button
						variant="secondary"
						size="sm"
						disabled={download.isPending}
						title="Download the log as a file (secret values are masked)"
					>
						{download.isPending ? <Spinner /> : <DownloadIcon aria-hidden />}
						{label}
					</Button>
				}
			/>
			<DropdownMenuContent align="start">
				{LOG_FILE_FORMATS.map((format) => (
					<DropdownMenuItem
						key={format.id}
						onClick={() => download.mutate(format.id)}
					>
						{format.label}
					</DropdownMenuItem>
				))}
			</DropdownMenuContent>
		</DropdownMenu>
	);
}
