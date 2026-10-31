import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ExternalLinkIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import {
	getServiceGitOptions,
	listGitConnectionsOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import { linkServiceGit, unlinkServiceGit } from "#/api/gen/sdk.gen";
import type { GitLinkView } from "#/api/gen/types.gen";
import { invalidateServices } from "#/api/invalidate";
import type { Service } from "#/api/models";
import { useConfirm } from "#/components/common/confirm-dialog";
import { Badge } from "#/components/ui/badge";
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
import { FieldDescription } from "#/components/ui/field";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { Switch } from "#/components/ui/switch";
import { Textarea } from "#/components/ui/textarea";
import { toast } from "#/components/ui/toast";
import { SettingRow } from "#/features/service-settings/fields";
import {
	formatWatchPaths,
	parseWatchPaths,
	providerInfo,
	watchPathProblem,
} from "./model";
import { ProviderIcon } from "./provider-icon";
import { invalidateGit } from "./queries";
import {
	RepoPicker,
	type RepoSelection,
	usableConnections,
} from "./repo-picker";

const WATCH_HELP =
	"One pattern per line, relative to the repository root: apps/api/** , packages/*/src , Dockerfile. A leading ! excludes (!**/*.md). Empty means every push deploys.";

function WatchPathsField({
	id,
	value,
	onChange,
	problem,
}: {
	id: string;
	value: string;
	onChange(value: string): void;
	problem: string | undefined;
}) {
	return (
		<div className="flex w-full flex-col gap-1.5">
			<Textarea
				id={id}
				rows={3}
				spellCheck={false}
				autoComplete="off"
				className="font-mono text-xs"
				placeholder={"apps/api/**\n!**/*.md"}
				aria-invalid={problem !== undefined}
				value={value}
				onChange={(event) => onChange(event.target.value)}
			/>
			<FieldDescription>{problem ?? WATCH_HELP}</FieldDescription>
		</div>
	);
}

/** Link (or relink) a service to a repository of a connection. */
function LinkDialog({
	service,
	link,
	open,
	onOpenChange,
}: {
	service: Service;
	link: GitLinkView | undefined;
	open: boolean;
	onOpenChange(open: boolean): void;
}) {
	const queryClient = useQueryClient();
	const connections = useQuery(listGitConnectionsOptions());
	const [selection, setSelection] = useState<RepoSelection | null>(null);
	const [autoDeploy, setAutoDeploy] = useState(true);
	const [watch, setWatch] = useState("");

	useEffect(() => {
		if (!open) return;
		setSelection(null);
		setAutoDeploy(link?.linked ? link.autoDeploy : true);
		setWatch(formatWatchPaths(link?.watchPaths));
	}, [open, link]);

	const paths = parseWatchPaths(watch);
	const problem = watchPathProblem(paths);
	const usable = usableConnections(connections.data?.items ?? []);
	const effective: RepoSelection | null =
		selection ??
		(usable[0]
			? { connectionId: usable[0].id, repo: "", branch: "", cloneUrl: null }
			: null);
	const ready =
		effective !== null && effective.repo !== "" && problem === undefined;

	const save = useMutation({
		mutationFn: async () => {
			if (!effective) return;
			await linkServiceGit({
				path: { serviceId: service.id },
				body: {
					connectionId: effective.connectionId,
					repo: effective.repo,
					...(effective.branch ? { branch: effective.branch } : {}),
					autoDeploy,
					watchPaths: paths,
				},
			});
		},
		onSuccess: async () => {
			toast.success(
				`${service.name} deploys from ${effective?.repo}`,
				autoDeploy
					? `Every push to ${effective?.branch || "its branch"} deploys it.`
					: "Auto-deploy is off: deploy it by hand.",
			);
			await Promise.all([
				invalidateGit(queryClient),
				invalidateServices(queryClient),
			]);
			onOpenChange(false);
		},
		onError: (e) =>
			toast.error("Could not link the repository", errorMessage(e)),
	});

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="lg">
				<DialogHeader>
					<DialogTitle>
						{link?.linked ? "Change repository" : "Pick a repository"}
					</DialogTitle>
					<DialogDescription>
						The clone URL and branch of {service.name} follow your pick (a
						staged change until you deploy). Auto-deploy applies right away.
					</DialogDescription>
				</DialogHeader>
				<DialogBody>
					{connections.isPending ? (
						<Skeleton className="h-40 rounded-card" />
					) : (
						<RepoPicker
							idPrefix="link"
							autoFocus
							connections={connections.data?.items ?? []}
							value={effective}
							onChange={setSelection}
						/>
					)}
					<div className="flex items-center justify-between gap-3">
						<span className="flex flex-col">
							<span className="font-medium text-ink-1 text-sm">
								Deploy on every push
							</span>
							<span className="type-secondary">
								A push to the branch starts a deployment of this service.
							</span>
						</span>
						<Switch
							checked={autoDeploy}
							onCheckedChange={setAutoDeploy}
							aria-label="Deploy on every push"
						/>
					</div>
					{autoDeploy ? (
						<div className="flex flex-col gap-1.5">
							<label
								htmlFor="link-watch"
								className="font-medium text-ink-1 text-sm"
							>
								Watch paths (monorepo)
							</label>
							<WatchPathsField
								id="link-watch"
								value={watch}
								onChange={setWatch}
								problem={problem}
							/>
						</div>
					) : null}
				</DialogBody>
				<DialogFooter>
					<Button variant="secondary" onClick={() => onOpenChange(false)}>
						Cancel
					</Button>
					<Button
						variant="primary"
						disabled={!ready || save.isPending}
						onClick={() => save.mutate()}
					>
						{save.isPending ? <Spinner /> : null}
						{link?.linked ? "Save" : "Link repository"}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}

function HookLine({ link }: { link: GitLinkView }) {
	const h = link.webhook;
	if (h.mode === "manual") {
		return (
			<span className="text-ink-3 text-xs leading-relaxed">
				Webhook: create it by hand at Bitbucket (URL and secret under Team
				settings → Integrations). Watch paths do not filter Bitbucket pushes.
			</span>
		);
	}
	return (
		<span className="flex flex-col gap-1">
			<span className="flex items-center gap-2 text-xs">
				<Badge
					tone={h.status === "active" ? "live" : "bad"}
					lamp
					shape="capsule"
				>
					{h.status === "active"
						? h.mode === "app"
							? "GitHub App webhook"
							: "Webhook registered"
						: "Webhook problem"}
				</Badge>
			</span>
			{h.message ? (
				<span className="text-destructive-text text-xs leading-relaxed">
					{h.message}
				</span>
			) : null}
		</span>
	);
}

/**
 * Rows of the Source section for a git service: the repository it is linked
 * to, auto-deploy on push and watch paths. These are operational settings:
 * they apply immediately and are never staged or rolled back. Picking another
 * repository changes the clone URL and branch, which are staged config.
 */
export function GitLinkRows({
	service,
	dirty,
}: {
	service: Service;
	/** The settings form has unsaved edits: picking a repository would clobber them. */
	dirty: boolean;
}) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const connections = useQuery(listGitConnectionsOptions());
	const linkQuery = useQuery(
		getServiceGitOptions({ path: { serviceId: service.id } }),
	);
	const link = linkQuery.data;
	const [picking, setPicking] = useState(false);
	const [watch, setWatch] = useState("");
	useEffect(() => {
		setWatch(formatWatchPaths(link?.watchPaths));
	}, [link?.watchPaths]);

	const refresh = () =>
		Promise.all([invalidateGit(queryClient), invalidateServices(queryClient)]);

	const update = useMutation({
		mutationFn: async (next: {
			autoDeploy?: boolean;
			watchPaths?: string[];
		}) => {
			if (!link?.connectionId) return;
			await linkServiceGit({
				path: { serviceId: service.id },
				body: { connectionId: link.connectionId, repo: link.repo, ...next },
			});
		},
		onSuccess: refresh,
		onError: (e) => toast.error("Could not save", errorMessage(e)),
	});

	const unlink = useMutation({
		mutationFn: async () => {
			await unlinkServiceGit({ path: { serviceId: service.id } });
		},
		onSuccess: async () => {
			toast.success(
				"Unlinked",
				`${service.name} keeps its repository URL but no longer deploys on push.`,
			);
			await refresh();
		},
		onError: (e) => toast.error("Could not unlink", errorMessage(e)),
	});

	if (linkQuery.isPending || connections.isPending) {
		return (
			<SettingRow label="Git provider">
				<Skeleton className="h-9 max-w-[360px] rounded-ctl" />
			</SettingRow>
		);
	}
	const usable = usableConnections(connections.data?.items ?? []);
	const paths = parseWatchPaths(watch);
	const problem = watchPathProblem(paths);
	const watchChanged = paths.join("\n") !== (link?.watchPaths ?? []).join("\n");

	if (!link?.linked) {
		return (
			<>
				<SettingRow
					label="Git provider"
					description="Pick the repository from a connected account to deploy on every push."
				>
					{usable.length === 0 ? (
						<span className="type-secondary">
							No provider is connected.{" "}
							<Link
								to="/settings/$section"
								params={{ section: "integrations" }}
								className="text-accent-ink underline-offset-2 hover:underline"
							>
								Connect GitHub, GitLab, Gitea, Forgejo or Bitbucket
							</Link>
							.
						</span>
					) : (
						<div className="flex flex-col gap-1.5">
							<div>
								<Button
									variant="secondary"
									size="sm"
									disabled={dirty}
									onClick={() => setPicking(true)}
								>
									Pick a repository
								</Button>
							</div>
							{dirty ? (
								<span className="type-secondary">
									Save or discard your edits first.
								</span>
							) : null}
						</div>
					)}
				</SettingRow>
				<LinkDialog
					service={service}
					link={link}
					open={picking}
					onOpenChange={setPicking}
				/>
			</>
		);
	}

	return (
		<>
			<SettingRow
				label="Git provider"
				description="The repository this service builds. Changing it changes the clone URL and branch below."
			>
				<div className="flex flex-col gap-2">
					<div className="flex flex-wrap items-center gap-2.5">
						<ProviderIcon provider={link.provider ?? "gitea"} size={24} />
						<span className="flex min-w-0 flex-col">
							<a
								href={link.repoUrl}
								target="_blank"
								rel="noreferrer"
								className="inline-flex items-center gap-1 truncate font-mono text-[13px] text-accent-ink hover:underline focus-visible:focus-ring"
							>
								{link.repo}
								<ExternalLinkIcon aria-hidden className="size-3" />
							</a>
							<span className="text-ink-3 text-xs">
								{link.connectionName} ·{" "}
								{providerInfo(link.provider ?? "gitea").label}
							</span>
						</span>
					</div>
					<HookLine link={link} />
					<div className="flex flex-wrap gap-2">
						<Button
							variant="secondary"
							size="sm"
							disabled={dirty}
							onClick={() => setPicking(true)}
						>
							Change repository
						</Button>
						<Button
							variant="ghost"
							size="sm"
							className="text-destructive-text"
							onClick={() =>
								void confirm({
									title: `Unlink ${service.name} from ${link.repo}?`,
									body: "It stops deploying on push and loses the connection's clone credentials. The repository URL and branch stay in its settings.",
									cta: "Unlink",
									danger: true,
									hold: false,
									onConfirm: async () => {
										await unlink.mutateAsync();
									},
								})
							}
						>
							Unlink
						</Button>
					</div>
					{dirty ? (
						<span className="type-secondary">
							Save or discard your edits before changing the repository.
						</span>
					) : null}
				</div>
			</SettingRow>
			<SettingRow
				label="Deploy on push"
				description="A push to the branch starts a deployment. Takes effect immediately; never staged."
			>
				<div className="flex flex-col gap-1.5">
					<Switch
						checked={link.autoDeploy}
						disabled={update.isPending}
						onCheckedChange={(checked) =>
							update.mutate({ autoDeploy: checked })
						}
						aria-label="Deploy on every push"
					/>
					{link.queuedCommit ? (
						<span className="type-secondary">
							A push ({link.queuedCommit.slice(0, 7)}) is waiting for the
							running deployment and deploys next.
						</span>
					) : null}
				</div>
			</SettingRow>
			<SettingRow
				label="Watch paths"
				htmlFor="set-watch-paths"
				description="Monorepos: only deploy when a changed file matches."
			>
				<WatchPathsField
					id="set-watch-paths"
					value={watch}
					onChange={setWatch}
					problem={problem}
				/>
				{watchChanged ? (
					<div className="flex gap-2">
						<Button
							variant="primary"
							size="sm"
							disabled={problem !== undefined || update.isPending}
							onClick={() => update.mutate({ watchPaths: paths })}
						>
							{update.isPending ? <Spinner /> : null}
							Save watch paths
						</Button>
						<Button
							variant="ghost"
							size="sm"
							onClick={() => setWatch(formatWatchPaths(link.watchPaths))}
						>
							Reset
						</Button>
					</div>
				) : null}
			</SettingRow>
			<LinkDialog
				service={service}
				link={link}
				open={picking}
				onOpenChange={setPicking}
			/>
		</>
	);
}
