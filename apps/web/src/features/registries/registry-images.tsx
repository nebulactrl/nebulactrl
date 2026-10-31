import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { errorMessage } from "#/api/client-config";
import { Relative } from "#/components/common/relative";
import { Segmented } from "#/components/ui/segmented";
import { Skeleton } from "#/components/ui/skeleton";
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "#/components/ui/table";
import { formatBytes } from "#/lib/format";
import { type ImageState, shortImageDigest } from "./model";
import { registryImagesQuery } from "./queries";

const STATES: { value: ImageState; label: string }[] = [
	{ value: "live", label: "Live" },
	{ value: "expired", label: "Expired" },
	{ value: "deleted", label: "Deleted" },
];

const STATE_NOTE: Record<ImageState, string> = {
	live: "Images a rollback can still pull.",
	expired:
		"Past the retention window; removed from the registry on its next cleanup.",
	deleted: "Removed from the registry.",
};

/** The images pushed to a registry: repository, digest, platform, size and when. */
export function RegistryImages({ registryId }: { registryId: string }) {
	const [state, setState] = useState<ImageState>("live");
	const images = useQuery(registryImagesQuery(registryId, state));

	return (
		<div className="flex flex-col gap-3">
			<div className="flex flex-wrap items-center justify-between gap-2">
				<Segmented
					value={state}
					onValueChange={setState}
					options={STATES}
					aria-label="Image state"
					font="sans"
				/>
				<span className="text-ink-3 text-xs">{STATE_NOTE[state]}</span>
			</div>
			{images.isPending ? (
				<Skeleton className="h-24 rounded-card" />
			) : images.isError ? (
				<p className="m-0 text-destructive-text text-xs">
					The images did not load: {errorMessage(images.error)}
				</p>
			) : images.data.length === 0 ? (
				<p className="m-0 text-ink-3 text-xs">
					{state === "live"
						? "Nothing has been pushed yet. The first deployment that builds on a build server pushes an image here."
						: `No ${state} images.`}
				</p>
			) : (
				<Table stack>
					<TableHeader>
						<TableRow>
							<TableHead>Repository</TableHead>
							<TableHead>Digest</TableHead>
							<TableHead>Platform</TableHead>
							<TableHead className="text-right">Size</TableHead>
							<TableHead>Pushed</TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						{images.data.map((image) => (
							<TableRow key={image.id}>
								<TableCell pin="start" className="max-w-[320px]">
									<span
										className="block truncate font-mono text-xs"
										title={image.repository}
									>
										{image.repository}
									</span>
								</TableCell>
								<TableCell label="Digest">
									<span
										className="font-mono text-ink-3 text-xs"
										title={image.digest}
									>
										{shortImageDigest(image.digest)}
									</span>
								</TableCell>
								<TableCell label="Platform">
									<span className="font-mono text-ink-3 text-xs">
										{image.platform}
									</span>
								</TableCell>
								<TableCell label="Size" className="text-right">
									<span className="font-mono text-ink-3 text-xs tabular-nums">
										{image.sizeBytes === null
											? "—"
											: formatBytes(image.sizeBytes)}
									</span>
								</TableCell>
								<TableCell label="Pushed">
									<span className="text-ink-3 text-xs">
										<Relative iso={image.pushedAt} />
									</span>
								</TableCell>
							</TableRow>
						))}
					</TableBody>
				</Table>
			)}
		</div>
	);
}
