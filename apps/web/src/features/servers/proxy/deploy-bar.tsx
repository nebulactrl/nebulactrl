import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { RocketIcon } from "lucide-react";
import { useState } from "react";
import { errorMessage } from "#/api/client-config";
import { deployServerProxy, planServerProxy } from "#/api/gen/sdk.gen";
import type { ProxyPlan, ServerProxy } from "#/api/gen/types.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { ToneIcon } from "#/components/common/tone-icon";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { Spinner } from "#/components/ui/spinner";
import { StatusIcon } from "#/components/ui/status-icon";
import { toast } from "#/components/ui/toast";
import { isAgentTooOld } from "#/features/proxy/lib/errors";
import {
	type PlanHighlights,
	planHighlights,
	planIsDisruptive,
} from "#/features/proxy/lib/proxy-plan";
import { invalidateProxy } from "#/features/proxy/queries";

export function PlanBody({ highlights }: { highlights: PlanHighlights }) {
	return (
		<div className="flex flex-col gap-3 text-left">
			{highlights.changes.length > 0 ? (
				<ul className="surface-well flex flex-col gap-1 rounded-ctl p-2.5 font-mono text-2xs text-ink-2">
					{highlights.changes.map((line) => (
						<li key={line} className="break-all">
							{line}
						</li>
					))}
				</ul>
			) : null}
			{highlights.restart.length > 0 ? (
				<div>
					<p className="font-medium text-ink-1 text-xs">
						The proxy restarts (about a second of dropped connections):
					</p>
					<ul className="mt-1 list-disc pl-4 text-ink-2 text-xs">
						{highlights.restart.map((reason) => (
							<li key={reason}>{reason}</li>
						))}
					</ul>
				</div>
			) : null}
			{highlights.warnings.map((warning) => (
				<Alert key={warning} variant="warning">
					<AlertDescription>{warning}</AlertDescription>
				</Alert>
			))}
		</div>
	);
}

/**
 * Sticks to the bottom of the Proxy tab while server-level changes
 * (certificates, upstream policy, tailnet) are staged. It asks the control
 * plane what a deploy would do, shows it, and runs it on confirm.
 */
export function DeployBar({
	serverId,
	serverName,
	proxy,
}: {
	serverId: string;
	serverName: string;
	proxy: ServerProxy;
}) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const [open, setOpen] = useState(false);
	const staged = proxy.generation > proxy.appliedGeneration;
	const inFlight =
		proxy.lastDeploy?.status === "pending" ||
		proxy.lastDeploy?.status === "sent" ||
		proxy.lastDeploy?.status === "acked";

	const plan = useQuery<ProxyPlan>({
		queryKey: ["proxy-plan", serverId, proxy.generation],
		queryFn: async ({ signal }) => {
			const { data } = await planServerProxy({
				path: { serverId },
				body: {},
				signal,
				throwOnError: true,
			});
			return data;
		},
		enabled: staged && !inFlight,
		staleTime: Number.POSITIVE_INFINITY,
	});

	const deploy = useMutation({
		mutationFn: async () => {
			await deployServerProxy({
				path: { serverId },
				body: {},
				headers: { "Idempotency-Key": crypto.randomUUID() },
			});
		},
		onSuccess: async () => {
			await invalidateProxy(queryClient);
			toast.deployed(
				`Deploying the proxy on ${serverName}`,
				"Status updates on this page.",
			);
		},
	});

	const highlights = plan.data ? planHighlights(plan.data) : null;
	const askDeploy = async () => {
		if (!plan.data || !highlights) return;
		await confirm({
			title: `Deploy the proxy on ${serverName}?`,
			body: <PlanBody highlights={highlights} />,
			cta: "Deploy proxy",
			danger: planIsDisruptive(plan.data),
			hold: false,
			onConfirm: async () => {
				await deploy.mutateAsync();
			},
		});
	};

	const failed = proxy.lastDeploy?.status === "failed";
	if (!staged && !inFlight && !failed) return null;

	const count = highlights?.changes.length ?? 0;
	return (
		<section
			aria-label="Deploy proxy"
			className="surface-float sticky bottom-0 z-10 -mx-1 flex flex-col gap-2 rounded-card px-4 py-3"
		>
			<div className="flex flex-wrap items-center gap-3" aria-live="polite">
				{inFlight ? <StatusIcon kind="deploying" label="" /> : null}
				{!inFlight && failed && !staged ? <ToneIcon tone="danger" /> : null}
				<div className="mr-auto min-w-0 text-sm">
					{inFlight ? (
						<span className="text-ink-1">
							Deploying the proxy… {proxy.lastDeploy?.message}
						</span>
					) : (
						<span className="text-ink-1">
							{staged
								? count > 0
									? `${count} staged proxy change${count === 1 ? "" : "s"}`
									: "Staged proxy changes"
								: "The last proxy deploy failed"}
							{highlights && highlights.restart.length > 0 ? (
								<span className="ml-2 text-warning-text text-xs">
									restarts the proxy
								</span>
							) : null}
						</span>
					)}
					{failed ? (
						<span className="mt-0.5 block text-destructive-text text-xs">
							{proxy.lastDeploy?.message || "The agent reported a failure."}
						</span>
					) : null}
				</div>
				{highlights && highlights.changes.length > 0 ? (
					<Button
						variant="ghost"
						size="sm"
						onClick={() => setOpen(!open)}
						aria-expanded={open}
					>
						{open ? "Hide plan" : "Show plan"}
					</Button>
				) : null}
				<Button
					variant="primary"
					disabled={!staged || inFlight || !plan.data || deploy.isPending}
					onClick={() => void askDeploy()}
				>
					{deploy.isPending || inFlight || plan.isPending ? (
						<Spinner />
					) : (
						<RocketIcon aria-hidden />
					)}
					Deploy proxy
				</Button>
			</div>
			{open && highlights ? <PlanBody highlights={highlights} /> : null}
			{plan.isError ? (
				<Alert variant="destructive">
					<AlertDescription>
						{isAgentTooOld(plan.error)
							? "The agent is too old for these changes. Upgrade it first."
							: `The plan did not load: ${errorMessage(plan.error)}`}
					</AlertDescription>
				</Alert>
			) : null}
			{deploy.isError ? (
				<Alert variant="destructive">
					<AlertDescription>{errorMessage(deploy.error)}</AlertDescription>
				</Alert>
			) : null}
		</section>
	);
}
