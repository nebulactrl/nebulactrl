import { useQuery } from "@tanstack/react-query";
import { cn } from "cn";
import { CheckIcon, LockIcon, SearchIcon } from "lucide-react";
import { useState } from "react";
import { errorMessage } from "#/api/client-config";
import {
	listGitBranchesOptions,
	listGitReposOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import type { ConnectionView, Repo } from "#/api/gen/types.gen";
import { Field, FieldDescription, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Skeleton } from "#/components/ui/skeleton";
import { providerInfo } from "./model";
import { ProviderIcon } from "./provider-icon";
import { useDebounced } from "./use-debounced";

/** What the picker chose: a repository of a connection and the branch to build. */
export interface RepoSelection {
	connectionId: string;
	repo: string;
	branch: string;
	/** The provider's clone URL when the picker knows it (null for typed names). */
	cloneUrl: string | null;
}

export interface RepoPickerProps {
	connections: readonly ConnectionView[];
	value: RepoSelection | null;
	onChange(next: RepoSelection | null): void;
	/** Prefix for element ids, so two pickers on a page do not clash. */
	idPrefix: string;
	/** Focus the search field when it mounts. */
	autoFocus?: boolean;
}

/** A connection the picker can use: finished, with credentials or a typed name. */
export function usableConnections(
	connections: readonly ConnectionView[],
): ConnectionView[] {
	return connections.filter((c) => c.status === "active");
}

function RepoRow({
	repo,
	selected,
	onPick,
}: {
	repo: Repo;
	selected: boolean;
	onPick(): void;
}) {
	return (
		<li>
			<button
				type="button"
				onClick={onPick}
				aria-pressed={selected}
				className={cn(
					"flex w-full cursor-pointer items-center gap-2.5 rounded-ctl border border-transparent px-2.5 py-2 text-left outline-none transition-colors duration-quick hover:bg-surface-hover focus-visible:focus-ring",
					selected && "border-line bg-surface-well shadow-well",
				)}
			>
				<span className="flex min-w-0 flex-1 flex-col">
					<span className="truncate font-medium text-ink-1 text-sm">
						{repo.fullName}
					</span>
					{repo.description ? (
						<span className="truncate text-ink-3 text-xs">
							{repo.description}
						</span>
					) : null}
				</span>
				{repo.private ? (
					<LockIcon
						aria-label="Private"
						className="size-3.5 shrink-0 text-ink-3"
					/>
				) : null}
				<span className="shrink-0 font-mono text-2xs text-ink-3">
					{repo.defaultBranch}
				</span>
				{selected ? (
					<CheckIcon aria-hidden className="size-4 shrink-0 text-accent-ink" />
				) : null}
			</button>
		</li>
	);
}

function BranchField({
	id,
	connection,
	repo,
	branch,
	onBranch,
	fallback,
}: {
	id: string;
	connection: ConnectionView;
	repo: string;
	branch: string;
	onBranch(branch: string): void;
	/** The repo's default branch, preselected when the list loads without the current one. */
	fallback: string;
}) {
	const listable = connection.canListRepos && repo !== "";
	const branches = useQuery({
		...listGitBranchesOptions({
			path: { connectionId: connection.id },
			query: { repo },
		}),
		enabled: listable,
		staleTime: 30_000,
	});
	const names = branches.data?.items ?? [];

	if (!listable || branches.isError) {
		return (
			<Field>
				<FieldLabel htmlFor={id}>Branch</FieldLabel>
				<Input
					id={id}
					autoComplete="off"
					spellCheck={false}
					className="font-mono"
					placeholder={fallback || "main"}
					value={branch}
					onChange={(event) => onBranch(event.target.value)}
				/>
				{branches.isError ? (
					<FieldDescription>
						Branches did not load ({errorMessage(branches.error)}). Type the
						name.
					</FieldDescription>
				) : null}
			</Field>
		);
	}
	const options =
		names.includes(branch) || branch === "" ? names : [branch, ...names];
	return (
		<Field>
			<FieldLabel htmlFor={id}>Branch</FieldLabel>
			{branches.isPending ? (
				<Skeleton className="h-9 rounded-ctl" />
			) : (
				<Select
					value={branch}
					items={options.map((name) => ({ value: name, label: name }))}
					onValueChange={(next) => onBranch(String(next ?? ""))}
				>
					<SelectTrigger id={id} className="w-full font-mono">
						<SelectValue placeholder={fallback || "main"} />
					</SelectTrigger>
					<SelectContent alignItemWithTrigger={false}>
						{options.map((name) => (
							<SelectItem key={name} value={name} className="font-mono">
								{name}
							</SelectItem>
						))}
					</SelectContent>
				</Select>
			)}
		</Field>
	);
}

/**
 * Pick a repository and branch from a git connection: a searchable list of
 * what the connection can reach (GitHub App installation, GitLab projects, a
 * Gitea or Forgejo account), or, for Bitbucket, a typed owner/name.
 */
export function RepoPicker({
	connections,
	value,
	onChange,
	idPrefix,
	autoFocus = false,
}: RepoPickerProps) {
	const usable = usableConnections(connections);
	const connection =
		usable.find((c) => c.id === value?.connectionId) ?? usable[0] ?? null;
	const [query, setQuery] = useState("");
	const debounced = useDebounced(query.trim());
	const listable = connection?.canListRepos === true;

	const repos = useQuery({
		...listGitReposOptions({
			path: { connectionId: connection?.id ?? "" },
			query: { q: debounced, limit: 50 },
		}),
		enabled: listable,
		staleTime: 15_000,
	});

	if (connection === null) return null;
	const repoName = value?.connectionId === connection.id ? value.repo : "";
	const branch = value?.connectionId === connection.id ? value.branch : "";
	const defaultBranch =
		repos.data?.items?.find((r) => r.fullName === repoName)?.defaultBranch ??
		"";

	const select = (next: Partial<RepoSelection>) =>
		onChange({
			connectionId: connection.id,
			repo: repoName,
			branch,
			cloneUrl: value?.connectionId === connection.id ? value.cloneUrl : null,
			...next,
		});

	return (
		<div className="flex flex-col gap-3">
			{usable.length > 1 ? (
				<Field>
					<FieldLabel htmlFor={`${idPrefix}-connection`}>Account</FieldLabel>
					<Select
						value={connection.id}
						items={usable.map((c) => ({ value: c.id, label: c.name }))}
						onValueChange={(next) =>
							onChange({
								connectionId: String(next),
								repo: "",
								branch: "",
								cloneUrl: null,
							})
						}
					>
						<SelectTrigger id={`${idPrefix}-connection`} className="w-full">
							<SelectValue />
						</SelectTrigger>
						<SelectContent alignItemWithTrigger={false}>
							{usable.map((c) => (
								<SelectItem key={c.id} value={c.id}>
									<span className="flex items-center gap-2">
										<ProviderIcon provider={c.provider} size={18} />
										{c.name}
										<span className="text-ink-3 text-xs">
											{providerInfo(c.provider).label}
										</span>
									</span>
								</SelectItem>
							))}
						</SelectContent>
					</Select>
				</Field>
			) : (
				<div className="flex items-center gap-2 text-ink-2 text-sm">
					<ProviderIcon provider={connection.provider} size={20} />
					<span className="font-medium text-ink-1">{connection.name}</span>
					{connection.accountLogin ? (
						<span className="text-ink-3 text-xs">
							as {connection.accountLogin}
						</span>
					) : null}
				</div>
			)}

			{listable ? (
				<Field>
					<FieldLabel htmlFor={`${idPrefix}-search`}>Repository</FieldLabel>
					<div className="relative">
						<SearchIcon
							aria-hidden
							className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-ink-3"
						/>
						<Input
							id={`${idPrefix}-search`}
							autoFocus={autoFocus}
							autoComplete="off"
							spellCheck={false}
							className="pl-8"
							placeholder="Search repositories"
							value={query}
							onChange={(event) => setQuery(event.target.value)}
						/>
					</div>
					<div
						className="surface-well max-h-52 overflow-y-auto overscroll-contain rounded-card p-1"
						aria-busy={repos.isFetching}
					>
						{repos.isPending ? (
							<Skeleton className="h-24 rounded-ctl" />
						) : repos.isError ? (
							<p className="m-0 p-3 text-bad text-xs">
								Repositories did not load: {errorMessage(repos.error)}
							</p>
						) : (repos.data?.items ?? []).length === 0 ? (
							<p className="m-0 p-3 text-ink-3 text-xs">
								{debounced === ""
									? "This connection can not see any repository yet. Install the GitHub App on a repository, or check the token's access."
									: `No repository matches "${debounced}".`}
							</p>
						) : (
							<ul className="m-0 flex list-none flex-col gap-0.5 p-0">
								{(repos.data?.items ?? []).map((r) => (
									<RepoRow
										key={r.fullName}
										repo={r}
										selected={r.fullName === repoName}
										onPick={() =>
											select({
												repo: r.fullName,
												branch: r.defaultBranch || "main",
												cloneUrl: r.cloneUrl,
											})
										}
									/>
								))}
							</ul>
						)}
					</div>
				</Field>
			) : (
				<Field>
					<FieldLabel htmlFor={`${idPrefix}-repo`}>Repository</FieldLabel>
					<Input
						id={`${idPrefix}-repo`}
						autoFocus={autoFocus}
						autoComplete="off"
						spellCheck={false}
						className="font-mono"
						placeholder="owner/repository"
						value={repoName}
						onChange={(event) =>
							select({
								repo: event.target.value.trim(),
								cloneUrl: null,
								branch: branch || "main",
							})
						}
					/>
					<FieldDescription>
						This connection can not list repositories: type its full name.
					</FieldDescription>
				</Field>
			)}

			{repoName !== "" ? (
				<BranchField
					id={`${idPrefix}-branch`}
					connection={connection}
					repo={repoName}
					branch={branch}
					fallback={defaultBranch}
					onBranch={(next) => select({ branch: next })}
				/>
			) : null}
		</div>
	);
}
