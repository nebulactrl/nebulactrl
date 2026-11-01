import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ExternalLinkIcon, GitPullRequestIcon, Trash2Icon } from "lucide-react";
import { environmentsQuery } from "#/api/hooks";
import type { Environment } from "#/api/models";
import { useConfirm } from "#/components/common/confirm-dialog";
import { Relative } from "#/components/common/relative";
import { Button } from "#/components/ui/button";
import { StatusIcon } from "#/components/ui/status-icon";
import { STATUS_KIND, STATUS_LABEL, statusDetail } from "./model";
import { useDeletePreview } from "./queries";
import { usePreviews } from "./use-previews";

function Banner({ environment }: { environment: Environment }) {
	const navigate = useNavigate();
	const confirm = useConfirm();
	const remove = useDeletePreview();
	const previews = usePreviews(environment.projectId);
	const environments = useQuery(environmentsQuery(environment.projectId));
	const preview = previews.data?.find(
		(p) => p.environmentId === environment.id,
	);
	const base = environments.data?.find(
		(e) => e.id === environment.parentEnvironmentId,
	);
	const detail = preview ? statusDetail(preview) : undefined;

	const askDelete = async () => {
		if (!preview) return;
		const ok = await confirm({
			title: `Delete the preview of PR #${preview.prNumber}?`,
			body: "Its services, containers, volumes and generated domains are removed, including the preview database. The pull request is not touched.",
			cta: "Delete preview",
			glyph: "trash",
			danger: true,
			hold: false,
		});
		if (!ok) return;
		await remove.mutateAsync(preview);
		if (base) {
			void navigate({
				to: "/p/$projectId/$envSlug",
				params: { projectId: environment.projectId, envSlug: base.slug },
			});
		}
	};

	return (
		<div className="pointer-events-none absolute inset-x-0 top-3 z-20 flex justify-center px-3">
			<div
				role="status"
				aria-label="Pull-request preview"
				className="surface-raised pointer-events-auto flex max-w-full min-w-0 items-center gap-3 rounded-card px-3 py-2"
			>
				<GitPullRequestIcon
					aria-hidden
					className="size-4 shrink-0 text-accent-ink"
				/>
				<div className="flex min-w-0 flex-col gap-0.5">
					<span className="flex min-w-0 items-center gap-2 text-[13px] text-ink-1 leading-none">
						<span className="truncate font-semibold">
							Preview{base ? ` of ${base.name}` : ""}
						</span>
						{preview ? (
							<span className="flex shrink-0 items-center gap-1.5 text-ink-2">
								<StatusIcon kind={STATUS_KIND[preview.status]} label="" />
								{STATUS_LABEL[preview.status]}
							</span>
						) : null}
					</span>
					<span className="flex min-w-0 items-center gap-2 text-[12px] text-ink-3 leading-none">
						{preview?.prUrl ? (
							<a
								href={preview.prUrl}
								target="_blank"
								rel="noreferrer"
								className="flex min-w-0 items-center gap-1 rounded-sm outline-none hover:text-ink-1 focus-visible:focus-ring"
							>
								<span className="truncate">{environment.name}</span>
								<ExternalLinkIcon aria-hidden className="size-3 shrink-0" />
							</a>
						) : (
							<span className="truncate">{environment.name}</span>
						)}
						{preview ? (
							<span className="shrink-0 font-mono text-[11px] text-ink-4">
								pushed <Relative iso={preview.lastPushAt} />
							</span>
						) : null}
					</span>
					{detail ? (
						<span
							className="max-w-[460px] truncate text-[12px] text-ink-3"
							title={detail}
						>
							{detail}
						</span>
					) : null}
				</div>
				<div className="flex shrink-0 items-center gap-1">
					{base ? (
						<Button
							size="sm"
							variant="ghost"
							render={
								<Link
									to="/p/$projectId/$envSlug"
									params={{
										projectId: environment.projectId,
										envSlug: base.slug,
									}}
								/>
							}
						>
							Open {base.name}
						</Button>
					) : null}
					<Button
						size="sm"
						variant="ghost"
						disabled={
							!preview || preview.status === "deleting" || remove.isPending
						}
						onClick={() => void askDelete()}
					>
						<Trash2Icon aria-hidden />
						Delete
					</Button>
				</div>
			</div>
		</div>
	);
}

/**
 * Above the canvas of a pull-request preview: which environment it was cloned
 * from, its pull request, status and last push, and a way to delete it.
 * Standard environments show nothing.
 */
export function PreviewBanner({ environment }: { environment: Environment }) {
	if (environment.kind !== "preview") return null;
	return <Banner environment={environment} />;
}
