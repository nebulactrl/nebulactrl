import { keepPreviousData, useQuery } from "@tanstack/react-query";
import {
	ArchiveIcon,
	ChevronRightIcon,
	DownloadIcon,
	FileIcon,
	FolderIcon,
	LinkIcon,
} from "lucide-react";
import { useEffect, useState } from "react";
import { errorCode, errorMessage } from "#/api/client-config";
import { listVolumeBackupFilesOptions } from "#/api/gen/@tanstack/react-query.gen";
import type { VolumeBackupView } from "#/api/gen/types.gen";
import { ToneIcon } from "#/components/common/tone-icon";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button, buttonVariants } from "#/components/ui/button";
import {
	Dialog,
	DialogBody,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "#/components/ui/dialog";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Skeleton } from "#/components/ui/skeleton";
import { absoluteTime, formatBytes } from "#/lib/format";
import {
	breadcrumbs,
	entryDownload,
	formatBackupTime,
	joinVolumePath,
	sortEntries,
	truncatedNotice,
	volumeFileUrl,
} from "./model";

function EntryIcon({ type }: { type: string }) {
	const cls = "size-4 shrink-0 text-ink-3";
	if (type === "dir") return <FolderIcon aria-hidden className={cls} />;
	if (type === "link") return <LinkIcon aria-hidden className={cls} />;
	return <FileIcon aria-hidden className={cls} />;
}

/**
 * Browse the files of one backup, a directory at a time. A file downloads as
 * it is, a folder (or the whole volume) as a .zip, both through the raw
 * download route. Nothing is restored from here.
 */
export function FilesDialog({
	backup,
	onClose,
}: {
	backup: VolumeBackupView | null;
	onClose: () => void;
}) {
	const volumes = backup?.volumes ?? [];
	const [volume, setVolume] = useState("");
	const [path, setPath] = useState("/");
	const backupId = backup?.id;
	const first = volumes[0] ?? "";
	// biome-ignore lint/correctness/useExhaustiveDependencies: a fresh browser per backup
	useEffect(() => {
		if (backupId === undefined) return;
		setVolume(first);
		setPath("/");
	}, [backupId]);

	const listing = useQuery({
		...listVolumeBackupFilesOptions({
			path: { backupId: backupId ?? "" },
			query: { volume, path },
		}),
		enabled: backupId !== undefined && volume !== "",
		placeholderData: keepPreviousData,
		retry: false,
	});

	const offline = errorCode(listing.error) === "server_offline";
	const entries = sortEntries(listing.data?.entries ?? []);
	const crumbs = breadcrumbs(volume, path);

	return (
		<Dialog open={backup !== null} onOpenChange={(o) => (o ? null : onClose())}>
			<DialogContent size="xl">
				<DialogHeader>
					<DialogTitle>Browse files</DialogTitle>
					<DialogDescription>
						{backup
							? `Backup of ${formatBackupTime(backup.createdAt)} (${absoluteTime(backup.createdAt)}). Files are read from the stored backup; nothing is restored.`
							: null}
					</DialogDescription>
				</DialogHeader>
				<DialogBody>
					<div className="flex flex-wrap items-center gap-2">
						{volumes.length > 1 ? (
							<Select
								value={volume}
								items={volumes.map((v) => ({ value: v, label: v }))}
								onValueChange={(v) => {
									setVolume(String(v ?? ""));
									setPath("/");
								}}
							>
								<SelectTrigger aria-label="Volume" className="w-44">
									<SelectValue placeholder="Volume" />
								</SelectTrigger>
								<SelectContent alignItemWithTrigger={false}>
									{volumes.map((v) => (
										<SelectItem key={v} value={v}>
											{v}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						) : null}
						<nav
							aria-label="Folder"
							className="flex min-w-0 flex-1 flex-wrap items-center gap-0.5 font-mono text-xs"
						>
							{crumbs.map((c, i) => (
								<span key={c.path} className="flex items-center gap-0.5">
									{i > 0 ? (
										<ChevronRightIcon
											aria-hidden
											className="size-3 text-ink-4"
										/>
									) : null}
									{i === crumbs.length - 1 ? (
										<span aria-current="page" className="px-1 text-ink-1">
											{c.label}
										</span>
									) : (
										<button
											type="button"
											className="rounded-sm px-1 text-accent-ink hover:underline focus-visible:focus-ring"
											onClick={() => setPath(c.path)}
										>
											{c.label}
										</button>
									)}
								</span>
							))}
						</nav>
						{backup && volume !== "" && !offline && !listing.isError ? (
							<a
								className={buttonVariants({ variant: "secondary", size: "sm" })}
								href={volumeFileUrl(backup.id, volume, path, true)}
								download
							>
								<ArchiveIcon aria-hidden />
								Download folder as .zip
							</a>
						) : null}
					</div>

					{offline ? (
						<Alert variant="warning">
							<AlertTitle>The server is offline</AlertTitle>
							<AlertDescription>
								Files are read through the server that holds the backup, so they
								can not be listed or downloaded until it is back. Nothing was
								changed.
							</AlertDescription>
						</Alert>
					) : listing.isError ? (
						<Alert variant="destructive">
							<AlertTitle>The files did not load</AlertTitle>
							<AlertDescription>{errorMessage(listing.error)}</AlertDescription>
						</Alert>
					) : listing.isPending && volume !== "" ? (
						<div className="flex flex-col gap-1.5">
							<Skeleton className="h-8" />
							<Skeleton className="h-8" />
							<Skeleton className="h-8" />
						</div>
					) : (
						<>
							{entries.length === 0 ? (
								<p className="py-2 text-ink-3 text-sm">This folder is empty.</p>
							) : (
								<ul
									aria-label="Files"
									className="max-h-[50vh] overflow-y-auto overscroll-contain border-line border-y"
								>
									{entries.map((e) => {
										const full = joinVolumePath(path, e.name);
										const download = entryDownload(e.type);
										return (
											<li
												key={e.name}
												className="flex items-center gap-3 border-line-faint border-b py-1.5 last:border-0"
											>
												<EntryIcon type={e.type} />
												{e.type === "dir" ? (
													<button
														type="button"
														className="min-w-0 flex-1 truncate text-left font-mono text-ink-1 text-xs hover:underline focus-visible:focus-ring"
														onClick={() => setPath(full)}
													>
														{e.name}
													</button>
												) : (
													<span
														className="min-w-0 flex-1 truncate font-mono text-ink-1 text-xs"
														title={e.name}
													>
														{e.name}
														{e.type === "link" ? (
															<span className="ml-2 font-sans text-ink-4">
																link
															</span>
														) : null}
													</span>
												)}
												<span className="w-20 shrink-0 text-right font-mono text-ink-3 text-xs tabular-nums">
													{e.type === "file" ? formatBytes(e.size) : ""}
												</span>
												<span
													className="hidden w-36 shrink-0 text-right text-ink-4 text-xs sm:block"
													title={e.mode}
												>
													{e.modifiedAt ? absoluteTime(e.modifiedAt) : ""}
												</span>
												{download && backup ? (
													<a
														className={buttonVariants({
															variant: "ghost",
															size: "icon-sm",
														})}
														href={volumeFileUrl(
															backup.id,
															volume,
															full,
															download === "archive",
														)}
														download
														aria-label={
															download === "archive"
																? `Download ${e.name} as .zip`
																: `Download ${e.name}`
														}
														title={
															download === "archive"
																? "Download as .zip"
																: "Download"
														}
													>
														{download === "archive" ? (
															<ArchiveIcon aria-hidden />
														) : (
															<DownloadIcon aria-hidden />
														)}
													</a>
												) : (
													<span aria-hidden className="size-7 shrink-0" />
												)}
											</li>
										);
									})}
								</ul>
							)}
							{listing.data?.truncated ? (
								<p className="flex items-start gap-1.5 text-status-staged-on text-xs">
									<ToneIcon tone="warning" className="mt-px size-3.5" />
									{truncatedNotice(entries.length)}
								</p>
							) : null}
						</>
					)}
				</DialogBody>
				<DialogFooter>
					<Button variant="ghost" onClick={onClose}>
						Close
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
