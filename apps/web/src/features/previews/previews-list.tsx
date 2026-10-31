import { useNavigate } from "@tanstack/react-router";
import { ExternalLinkIcon, GitPullRequestIcon, Trash2Icon } from "lucide-react";
import { errorMessage } from "#/api/client-config";
import type { PreviewView } from "#/api/gen/types.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
import { Button } from "#/components/ui/button";
import { Skeleton, SkeletonReveal } from "#/components/ui/skeleton";
import { StatusIcon } from "#/components/ui/status-icon";
import {
	Table,
	TableActionsCell,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "#/components/ui/table";
import { STATUS_KIND, STATUS_LABEL, statusDetail } from "./model";
import { useDeletePreview } from "./queries";
import { usePreviews } from "./use-previews";

function Row({ preview }: { preview: PreviewView }) {
	const navigate = useNavigate();
	const confirm = useConfirm();
	const remove = useDeletePreview();
	const deleting = preview.status === "deleting";
	const detail = statusDetail(preview);

	const open = () =>
		void navigate({
			to: "/p/$projectId/$envSlug",
			params: {
				projectId: preview.projectId,
				envSlug: preview.environmentSlug,
			},
		});

	const askDelete = async () => {
		const ok = await confirm({
			title: `Delete the preview of PR #${preview.prNumber}?`,
			body: "Its services, containers, volumes and generated domains are removed, including the preview database and everything in it. The pull request itself is not touched.",
			cta: "Delete preview",
			glyph: "trash",
			danger: true,
			hold: false,
		});
		if (ok) remove.mutate(preview);
	};

	return (
		<TableRow
			className={deleting ? "opacity-60" : "cursor-pointer"}
			onClick={(event) => {
				if (deleting || (event.target as HTMLElement).closest("a,button"))
					return;
				open();
			}}
		>
			<TableCell pin="end" className="h-12 whitespace-nowrap">
				<span className="flex items-center gap-2">
					<StatusIcon kind={STATUS_KIND[preview.status]} label="" />
					<span className="text-[13px] text-ink-1">
						{STATUS_LABEL[preview.status]}
					</span>
				</span>
			</TableCell>
			<TableCell pin="start" label="Pull request" className="max-w-[360px]">
				<span className="flex min-w-0 flex-col gap-[3px]">
					<span className="flex min-w-0 items-center gap-1.5">
						<GitPullRequestIcon
							aria-hidden
							className="size-3.5 shrink-0 text-ink-3"
						/>
						<span className="truncate font-semibold text-[13px] text-ink-1">
							#{preview.prNumber} {preview.prTitle}
						</span>
						{preview.fromFork ? (
							<span className="shrink-0 rounded-md border border-line-faint bg-white/5 px-1.5 py-[3px] font-mono text-3xs text-ink-3">
								fork
							</span>
						) : null}
					</span>
					<span className="truncate font-mono text-[11px] text-ink-4">
						{preview.repo} · {preview.headBranch}
						{preview.headSha ? ` · ${preview.headSha.slice(0, 7)}` : ""}
					</span>
					{detail ? (
						<span className="truncate text-[12px] text-ink-3" title={detail}>
							{detail}
						</span>
					) : null}
				</span>
			</TableCell>
			<TableCell label="URLs" className="max-w-[260px]">
				<span className="flex min-w-0 flex-col gap-0.5">
					{(preview.urls ?? []).length === 0 ? (
						<span className="text-[12px] text-ink-4">—</span>
					) : (
						(preview.urls ?? []).map((url) => (
							<a
								key={url}
								href={url}
								target="_blank"
								rel="noreferrer"
								className="flex min-w-0 items-center gap-1 rounded-sm font-mono text-[12px] text-accent-ink outline-none hover:underline focus-visible:focus-ring"
							>
								<span className="truncate">{url.replace("https://", "")}</span>
								<ExternalLinkIcon aria-hidden className="size-3 shrink-0" />
							</a>
						))
					)}
				</span>
			</TableCell>
			<TableCell label="Age">
				<span className="whitespace-nowrap font-mono text-[12px] text-ink-3">
					<Relative iso={preview.createdAt} />
				</span>
			</TableCell>
			<TableCell label="Last push">
				<span className="whitespace-nowrap font-mono text-[12px] text-ink-3">
					<Relative iso={preview.lastPushAt} />
				</span>
			</TableCell>
			<TableActionsCell>
				<div className="flex items-center justify-end gap-1">
					{preview.prUrl ? (
						<Button
							size="sm"
							variant="ghost"
							render={
								<a href={preview.prUrl} target="_blank" rel="noreferrer" />
							}
						>
							<ExternalLinkIcon aria-hidden />
							PR
						</Button>
					) : null}
					<Button
						size="sm"
						variant="ghost"
						disabled={deleting || remove.isPending}
						aria-label={`Delete the preview of PR #${preview.prNumber}`}
						onClick={() => void askDelete()}
					>
						<Trash2Icon aria-hidden />
						Delete
					</Button>
				</div>
			</TableActionsCell>
		</TableRow>
	);
}

/**
 * The project's pull-request previews: status, age, last push and a delete
 * action. A row opens the preview's canvas.
 */
export function PreviewsList({ projectId }: { projectId: string }) {
	const previews = usePreviews(projectId);

	return (
		<SkeletonReveal
			loading={previews.data === undefined && !previews.isError}
			skeleton={
				<div role="status" aria-label="Loading previews">
					<Skeleton className="h-40 rounded-card" />
				</div>
			}
		>
			{() => {
				if (previews.data === undefined) {
					return (
						<ErrorState
							frame="raised"
							title="Couldn't load previews"
							body={errorMessage(previews.error)}
							retry={() => void previews.refetch()}
						/>
					);
				}
				if (previews.data.length === 0) {
					return (
						<EmptyState
							illustration="box"
							title="No previews yet"
							body="When a pull request opens against a connected repository, it gets its own copy of this project with a fresh database and its own URLs. They show up here."
							frame="dashed"
						/>
					);
				}
				return (
					<Table stack>
						<TableHeader>
							<TableRow>
								<TableHead>Status</TableHead>
								<TableHead>Pull request</TableHead>
								<TableHead>URLs</TableHead>
								<TableHead>Age</TableHead>
								<TableHead>Last push</TableHead>
								<TableHead className="w-0">
									<span className="sr-only">Actions</span>
								</TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{previews.data.map((preview) => (
								<Row key={preview.id} preview={preview} />
							))}
						</TableBody>
					</Table>
				);
			}}
		</SkeletonReveal>
	);
}
