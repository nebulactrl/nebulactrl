import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { errorMessage, errorStatus } from "#/api/client-config";
import { getApprovalOptions } from "#/api/gen/@tanstack/react-query.gen";
import { approveApproval, declineApproval } from "#/api/gen/sdk.gen";
import type { ApprovalView } from "#/api/gen/types.gen";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
import { PageFrame } from "#/components/shell/page-frame";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "#/components/ui/card";
import { KvRow } from "#/components/ui/kv-row";
import type { LampTone } from "#/components/ui/lamp";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { absoluteTime } from "#/lib/format";
import { useDocumentTitle } from "#/lib/hooks/use-document-title";
import { approvalStatus, requesterText } from "./model";

const STATUS_TONE: Record<ApprovalView["status"], LampTone> = {
	pending: "warn",
	approved: "ok",
	declined: "bad",
	used: "off",
	expired: "off",
	stale: "off",
};

/**
 * One request from an AI client (or an opted-in API token) to change a
 * production environment. It shows exactly what will happen, who asked and
 * when the request expires, and lets an Owner, Admin or Developer with deploy
 * rights approve it once or decline it. The waiting call, or its retry, finds
 * the decision; nothing here runs the change itself.
 */
export function ApprovalPage({ approvalId }: { approvalId: string }) {
	useDocumentTitle(["Approve a production change"]);
	const queryClient = useQueryClient();
	const options = getApprovalOptions({ path: { approvalId } });
	const query = useQuery({
		...options,
		retry: false,
		// A pending request can be decided elsewhere or expire while the page is open.
		refetchInterval: (q) =>
			q.state.data?.status === "pending" ? 10_000 : false,
	});

	const decide = useMutation({
		mutationFn: async (approve: boolean) => {
			const call = approve ? approveApproval : declineApproval;
			const { data } = await call({ path: { approvalId } });
			return { data, approve };
		},
		onSuccess: ({ data, approve }) => {
			queryClient.setQueryData(options.queryKey, data);
			if (approve) {
				toast.success(
					"Approved",
					"The agent can repeat the call now. It runs once, exactly as shown.",
				);
			} else {
				toast.info("Declined", "The change will not run.");
			}
		},
		onError: (error) => {
			toast.error("Could not record your decision", errorMessage(error));
			void query.refetch();
		},
	});

	if (query.isPending) {
		return (
			<PageFrame page="approvals" title="Approve a production change">
				<Skeleton className="h-72 max-w-[720px] rounded-card" />
			</PageFrame>
		);
	}
	if (!query.data) {
		const gone = errorStatus(query.error) === 404;
		return (
			<PageFrame page="approvals" title="Approve a production change">
				<ErrorState
					frame="raised"
					neutral
					title={
						gone
							? "This approval request was not found"
							: "The approval request did not load"
					}
					body={
						gone
							? "It does not exist, or it belongs to a project you cannot see. Ask the agent to try again."
							: "The control plane did not answer, so nothing was changed. Try again in a moment."
					}
					{...(gone
						? {
								action: (
									<Button variant="secondary" render={<Link to="/" />}>
										Go to projects
									</Button>
								),
							}
						: { retry: () => void query.refetch() })}
				/>
			</PageFrame>
		);
	}

	const a = query.data;
	const status = approvalStatus(a);
	const targets = a.plan.targets ?? [];
	const effects = a.plan.effects ?? [];
	return (
		<PageFrame
			page="approvals"
			title="Approve a production change"
			description={`${a.plan.project} / ${a.plan.environment}`}
		>
			<Card className="max-w-[720px]" data-testid="approval">
				<CardHeader>
					<CardTitle className="flex flex-wrap items-center gap-2.5">
						<span className="min-w-0 flex-1">{a.plan.summary}</span>
						<Badge tone={STATUS_TONE[a.status]} shape="capsule">
							{status.label}
						</Badge>
					</CardTitle>
				</CardHeader>
				<CardContent className="flex flex-col gap-4">
					{effects.length > 0 ? (
						<ul className="flex list-disc flex-col gap-1.5 pl-5 text-ink-2 text-sm leading-snug">
							{effects.map((effect) => (
								<li key={effect}>{effect}</li>
							))}
						</ul>
					) : null}
					{a.plan.irreversible ? (
						<Alert variant="warning">
							<AlertTitle>This cannot be undone</AlertTitle>
							<AlertDescription>
								Approve it only if you want exactly this to happen in
								production.
							</AlertDescription>
						</Alert>
					) : null}
					<div>
						<KvRow label="Asked by" rule="top">
							<span
								className="text-ink-1 text-sm"
								data-testid="approval-requester"
							>
								{requesterText(a.requester)}
							</span>
						</KvRow>
						{targets.length > 0 ? (
							<KvRow label="Affects">
								<span className="flex flex-wrap gap-1.5">
									{targets.map((t) => (
										<Badge key={t.id} variant="outline" shape="capsule">
											{t.kind} {t.name}
										</Badge>
									))}
								</span>
							</KvRow>
						) : null}
						<KvRow label="Requested">
							<span
								className="text-ink-2 text-sm"
								title={absoluteTime(a.createdAt)}
							>
								<Relative iso={a.createdAt} />
							</span>
						</KvRow>
						<KvRow
							label="Expires"
							description="A request lives ten minutes, and is void if the service changes first."
						>
							<span
								className="text-ink-2 text-sm"
								title={absoluteTime(a.expiresAt)}
							>
								{absoluteTime(a.expiresAt)} · <Relative iso={a.expiresAt} />
							</span>
						</KvRow>
						{a.decidedBy ? (
							<KvRow label="Decided by">
								<span className="text-ink-2 text-sm">
									{a.decidedBy}
									{a.decidedAt ? (
										<>
											{" "}
											· <Relative iso={a.decidedAt} />
										</>
									) : null}
								</span>
							</KvRow>
						) : null}
					</div>
					<p
						className="text-ink-3 text-sm leading-relaxed"
						data-testid="approval-status"
					>
						{status.detail}
					</p>
					{a.status === "pending" && !a.canDecide ? (
						<Alert variant="info">
							<AlertTitle>
								You can read this request but not decide it
							</AlertTitle>
							<AlertDescription>
								Approving needs an Owner, Admin or Developer with deploy rights
								on {a.plan.project}.
							</AlertDescription>
						</Alert>
					) : null}
					{a.canDecide ? (
						<div className="flex flex-wrap justify-end gap-2.5">
							<Button
								variant="ghost"
								disabled={decide.isPending}
								onClick={() => decide.mutate(false)}
							>
								Decline
							</Button>
							<Button
								variant="primary"
								disabled={decide.isPending}
								onClick={() => decide.mutate(true)}
							>
								{decide.isPending ? <Spinner /> : null}
								Approve change
							</Button>
						</div>
					) : null}
				</CardContent>
			</Card>
		</PageFrame>
	);
}
