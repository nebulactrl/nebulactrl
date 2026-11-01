import { useQuery } from "@tanstack/react-query";
import { DownloadIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import type { Environment } from "#/api/models";
import { CopyButton } from "#/components/common/copy-button";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import {
	Dialog,
	DialogBody,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "#/components/ui/dialog";
import { Segmented } from "#/components/ui/segmented";
import { Skeleton } from "#/components/ui/skeleton";
import { Textarea } from "#/components/ui/textarea";
import { EXPORT_FILE_NAME, exportWarning } from "./model";
import { exportConfig } from "./queries";

type Scope = "environment" | "all";

function download(file: string): void {
	const url = URL.createObjectURL(
		new Blob([file], { type: "text/plain;charset=utf-8" }),
	);
	const anchor = document.createElement("a");
	anchor.href = url;
	anchor.download = EXPORT_FILE_NAME;
	anchor.click();
	URL.revokeObjectURL(url);
}

/**
 * "Export nebula.toml": the live project as a file, for this environment or
 * for all of them (one base plus an override block per other environment).
 * Defaults are left out, secrets are names only, and what the format cannot
 * say is listed as a warning instead of being dropped silently.
 */
export function ExportDialog({
	environment,
	open,
	onOpenChange,
}: {
	environment: Environment;
	open: boolean;
	onOpenChange(open: boolean): void;
}) {
	const [scope, setScope] = useState<Scope>("environment");
	useEffect(() => {
		if (open) setScope("environment");
	}, [open]);

	const target = scope === "all" ? "all" : environment.slug;
	const result = useQuery({
		queryKey: ["project-config-export", environment.projectId, target],
		queryFn: () =>
			exportConfig({ projectId: environment.projectId, environment: target }),
		enabled: open,
		// A file is a snapshot of now; never serve an earlier one.
		gcTime: 0,
		staleTime: 0,
		refetchOnWindowFocus: false,
		retry: false,
	});
	const file = result.data?.file ?? "";
	const warnings = (result.data?.warnings ?? [])
		.map(exportWarning)
		.filter((warning) => warning.message !== "");

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="lg" showCloseButton>
				<DialogHeader>
					<DialogTitle>Export nebula.toml</DialogTitle>
					<DialogDescription>
						Your current setup as a project file. Commit it to your repository
						and it is read on every push. Secret values are never written, only
						their names.
					</DialogDescription>
				</DialogHeader>
				<DialogBody className="flex flex-col gap-3">
					<Segmented<Scope>
						aria-label="What to export"
						font="sans"
						density="regular"
						value={scope}
						onValueChange={setScope}
						options={[
							{ value: "environment", label: environment.name },
							{ value: "all", label: "All environments" },
						]}
					/>
					{result.isPending ? (
						<div role="status" aria-label="Exporting">
							<Skeleton className="h-40 rounded-ctl" />
						</div>
					) : null}
					{result.isError ? (
						<Alert variant="destructive">
							<AlertTitle>Could not export</AlertTitle>
							<AlertDescription>{errorMessage(result.error)}</AlertDescription>
						</Alert>
					) : null}
					{result.isSuccess ? (
						<Textarea
							readOnly
							spellCheck={false}
							aria-label="nebula.toml"
							value={file}
							rows={Math.min(Math.max(file.split("\n").length, 6), 18)}
							className="max-h-[min(46vh,420px)] font-mono text-xs leading-[1.5]"
						/>
					) : null}
					{warnings.length > 0 ? (
						<Alert variant="warning" aria-label="Export warnings">
							<AlertTitle>
								{warnings.length === 1
									? "One thing to know"
									: `${warnings.length} things to know`}
							</AlertTitle>
							<AlertDescription>
								<ul className="mt-1 flex flex-col gap-1">
									{warnings.map((warning) => (
										<li key={`${warning.code}:${warning.message}`}>
											{warning.message}
											{warning.code ? (
												<span className="ml-2 font-mono text-2xs text-ink-4">
													{warning.code}
												</span>
											) : null}
										</li>
									))}
								</ul>
							</AlertDescription>
						</Alert>
					) : null}
				</DialogBody>
				<DialogFooter>
					<Button variant="secondary" onClick={() => onOpenChange(false)}>
						Close
					</Button>
					<CopyButton value={file} variant="secondary">
						Copy
					</CopyButton>
					<Button
						variant="primary"
						disabled={!result.isSuccess || file === ""}
						onClick={() => download(file)}
					>
						<DownloadIcon aria-hidden />
						Download
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
