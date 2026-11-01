import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRoundIcon, PlusIcon } from "lucide-react";
import { errorMessage } from "#/api/client-config";
import {
	listTokensOptions,
	listTokensQueryKey,
} from "#/api/gen/@tanstack/react-query.gen";
import { revokeToken } from "#/api/gen/sdk.gen";
import type { Token } from "#/api/gen/types.gen";
import { projectsQuery } from "#/api/hooks";
import { useConfirm } from "#/components/common/confirm-dialog";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { Skeleton, SkeletonReveal } from "#/components/ui/skeleton";
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "#/components/ui/table";
import { toast } from "#/components/ui/toast";
import { absoluteTime } from "#/lib/format";
import { CliInstall } from "./cli-install";
import { describeScopes, tokenState } from "./token-presets";

const STATE_TONE = { active: "ok", revoked: "bad", expired: "warn" } as const;

function TokenRow({
	token,
	projectNames,
	onRevoke,
}: {
	token: Token;
	projectNames: ReadonlyMap<string, string>;
	onRevoke: (token: Token) => void;
}) {
	const state = tokenState(token);
	const restricted = token.projectIds ?? [];
	return (
		<TableRow>
			<TableCell pin="start">
				<span className="flex flex-col gap-0.5">
					<span
						className={
							state === "active"
								? "font-medium text-ink-1"
								: "font-medium text-ink-2 line-through"
						}
					>
						{token.name}
					</span>
					<span className="font-mono text-2xs text-ink-3">
						nct_{token.prefix}…
					</span>
				</span>
			</TableCell>
			<TableCell label="Access">
				<span className="flex flex-col gap-0.5">
					<span
						title={(token.scopes ?? []).join(", ")}
						className="text-ink-2 text-xs"
					>
						{describeScopes(token.scopes)}
					</span>
					<span className="text-ink-3 text-2xs">
						{token.kind === "mcp" ? "MCP agent" : "REST API and CLI"}
					</span>
					{token.allowProductionWithoutApproval ? (
						<span className="text-warning-text text-2xs">
							Skips production approval
						</span>
					) : (
						<span className="text-ink-3 text-2xs">
							Production changes need approval
						</span>
					)}
				</span>
			</TableCell>
			<TableCell label="Projects" className="text-ink-3 text-xs">
				{restricted.length === 0
					? "All projects"
					: restricted
							.map((id) => projectNames.get(id) ?? "unknown project")
							.join(", ")}
			</TableCell>
			<TableCell label="Last used" className="text-ink-3 text-xs">
				{token.lastUsedAt ? <Relative iso={token.lastUsedAt} /> : "never"}
			</TableCell>
			<TableCell label="Expires" className="text-ink-3 text-xs">
				{token.expiresAt ? (
					<span title={absoluteTime(token.expiresAt)}>
						{state === "expired" ? "expired " : ""}
						<Relative iso={token.expiresAt} />
					</span>
				) : (
					"never"
				)}
			</TableCell>
			<TableCell label="State" pin="end">
				<Badge tone={STATE_TONE[state]} lamp shape="capsule">
					{state}
				</Badge>
			</TableCell>
			<TableCell className="w-px">
				{state === "revoked" ? null : (
					<Button
						variant="ghost"
						size="sm"
						className="text-destructive-text"
						onClick={() => onRevoke(token)}
					>
						Revoke
					</Button>
				)}
			</TableCell>
		</TableRow>
	);
}

/** API tokens: list, revoke, and the entry to the create dialog. The secret shows once. */
export function TokensSection({ onCreate }: { onCreate: () => void }) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const tokens = useQuery(listTokensOptions());
	const projects = useQuery(projectsQuery());
	const projectNames = new Map(
		(projects.data ?? []).map((p) => [p.id, p.name]),
	);

	const revoke = useMutation({
		mutationFn: async (token: Token) => {
			await revokeToken({ path: { id: token.id } });
			return token;
		},
		onSuccess: async (token) => {
			toast.success(`Revoked ${token.name}`, "It stops working immediately.");
			await queryClient.invalidateQueries({ queryKey: listTokensQueryKey() });
		},
		onError: (error) =>
			toast.error("Could not revoke the token", errorMessage(error)),
	});

	const items = tokens.data?.items ?? [];

	const askRevoke = (token: Token) =>
		void confirm({
			title: `Revoke ${token.name}?`,
			body: "Anything using this token loses access immediately. This cannot be undone; create a new token to replace it.",
			cta: "Revoke token",
			danger: true,
			hold: false,
			onConfirm: async () => {
				await revoke.mutateAsync(token);
			},
		});

	return (
		<div className="flex flex-col gap-6">
			<CliInstall />
			<SkeletonReveal
				loading={tokens.isPending}
				skeleton={<Skeleton className="h-40 rounded-card" />}
			>
				{() => (
					<TokensBody
						failed={tokens.isError && !tokens.data}
						items={items}
						projectNames={projectNames}
						onCreate={onCreate}
						onRevoke={askRevoke}
						retry={() => void tokens.refetch()}
					/>
				)}
			</SkeletonReveal>
		</div>
	);
}

function TokensBody({
	failed,
	items,
	projectNames,
	onCreate,
	onRevoke,
	retry,
}: {
	failed: boolean;
	items: readonly Token[];
	projectNames: ReadonlyMap<string, string>;
	onCreate: () => void;
	onRevoke: (token: Token) => void;
	retry: () => void;
}) {
	if (failed) {
		return (
			<ErrorState
				frame="raised"
				title="Tokens did not load"
				body="The control plane did not answer, so nothing was changed. Try again in a moment."
				retry={retry}
			/>
		);
	}
	if (items.length === 0) {
		return (
			<EmptyState
				illustration="key"
				title="Create your first API token"
				body="Use one for the CLI, a CI job or an MCP client. Scope it to what it needs and to the projects it should see."
				action={
					<Button variant="primary" onClick={onCreate}>
						<PlusIcon />
						New token
					</Button>
				}
			/>
		);
	}

	return (
		<div className="flex flex-col gap-4">
			<div className="flex flex-wrap items-center justify-between gap-3">
				<p className="flex items-center gap-2 text-ink-3 text-xs">
					<KeyRoundIcon aria-hidden="true" className="size-4" />
					Tokens authenticate as{" "}
					<code className="type-mono text-ink-2">Authorization: Bearer</code>
				</p>
				<Button variant="primary" onClick={onCreate}>
					<PlusIcon />
					New token
				</Button>
			</div>
			<div className="overflow-hidden border-line border-y">
				<Table stack>
					<TableHeader>
						<TableRow>
							<TableHead>Name</TableHead>
							<TableHead>Access</TableHead>
							<TableHead>Projects</TableHead>
							<TableHead>Last used</TableHead>
							<TableHead>Expires</TableHead>
							<TableHead>State</TableHead>
							<TableHead className="w-px" />
						</TableRow>
					</TableHeader>
					<TableBody>
						{items.map((token) => (
							<TokenRow
								key={token.id}
								token={token}
								projectNames={projectNames}
								onRevoke={onRevoke}
							/>
						))}
					</TableBody>
				</Table>
			</div>
		</div>
	);
}
