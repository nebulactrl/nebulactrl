import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { PlusIcon, SendIcon } from "lucide-react";
import { useState } from "react";
import { errorCode, errorMessage } from "#/api/client-config";
import {
	deleteLogDrain,
	testLogDrain,
	updateLogDrain,
} from "#/api/gen/sdk.gen";
import type { LogDrainView } from "#/api/gen/types.gen";
import { projectsQuery } from "#/api/hooks";
import { useConfirm } from "#/components/common/confirm-dialog";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { Button } from "#/components/ui/button";
import { Skeleton } from "#/components/ui/skeleton";
import { toast } from "#/components/ui/toast";
import { DrainCard } from "./drain-card";
import { DrainDialog } from "./drain-dialog";
import {
	orderDrains,
	type TestOutcome,
	testFailure,
	testOutcome,
	toggleToast,
} from "./model";
import { invalidateLogDrains, logDrainsQuery } from "./queries";

/**
 * Team settings → Log forwarding: the built-in central logs (a switch, off
 * until the team turns it on) and the team's own drains to Loki, an OTLP/HTTP
 * collector, syslog or an HTTP endpoint (Datadog, Better Stack, Axiom). Each
 * server ships its own logs from its own host; the cards show every server's
 * delivery state, what was sent, dropped and is waiting.
 */
export function LogDrainsSection() {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const drains = useQuery(logDrainsQuery());
	const projects = useQuery(projectsQuery());
	const projectNames = new Map(
		(projects.data ?? []).map((p) => [p.id, p.name]),
	);

	const [creating, setCreating] = useState(false);
	const [editing, setEditing] = useState<LogDrainView | null>(null);
	const [testingId, setTestingId] = useState<string | null>(null);
	const [outcomes, setOutcomes] = useState<Record<string, TestOutcome>>({});

	const setOutcome = (id: string, outcome: TestOutcome | undefined) =>
		setOutcomes((current) => {
			const next = { ...current };
			if (outcome) next[id] = outcome;
			else delete next[id];
			return next;
		});

	const toggle = useMutation({
		mutationFn: async ({
			drain,
			enabled,
		}: {
			drain: LogDrainView;
			enabled: boolean;
		}) => {
			await updateLogDrain({
				path: { drainId: drain.id },
				body: { enabled },
				throwOnError: true,
			});
			return { drain, enabled };
		},
		onSuccess: async ({ drain, enabled }) => {
			const message = toggleToast(drain, enabled);
			toast.success(message.title, message.body);
			await invalidateLogDrains(queryClient);
		},
		onError: (e) => toast.error("Could not change the drain", errorMessage(e)),
	});

	const test = useMutation({
		mutationFn: async (drain: LogDrainView) => {
			setTestingId(drain.id);
			setOutcome(drain.id, undefined);
			const { data } = await testLogDrain({
				path: { drainId: drain.id },
				body: {},
				throwOnError: true,
			});
			return { drain, result: data };
		},
		onSuccess: async ({ drain, result }) => {
			setOutcome(drain.id, testOutcome(result));
			await invalidateLogDrains(queryClient);
		},
		onError: (e, drain) =>
			setOutcome(
				drain.id,
				testFailure({ code: errorCode(e), message: errorMessage(e) }),
			),
		onSettled: () => setTestingId(null),
	});

	const remove = useMutation({
		mutationFn: async (drain: LogDrainView) => {
			await deleteLogDrain({
				path: { drainId: drain.id },
				throwOnError: true,
			});
			return drain;
		},
		onSuccess: async (drain) => {
			setOutcome(drain.id, undefined);
			toast.success(`Deleted ${drain.name}`, "Servers stop shipping to it.");
			await invalidateLogDrains(queryClient);
		},
		onError: (e) => toast.error("Could not delete the drain", errorMessage(e)),
	});

	if (drains.isPending) return <Skeleton className="h-40 rounded-card" />;
	if (drains.isError && !drains.data) {
		return (
			<ErrorState
				frame="raised"
				title="Log drains did not load"
				body="Log drains are for team admins. Check your role and session and try again; nothing was changed."
				retry={() => void drains.refetch()}
			/>
		);
	}
	const items = orderDrains(drains.data?.items ?? []);
	const own = items.filter((drain) => !drain.builtin).length;
	const add = (
		<Button variant="primary" onClick={() => setCreating(true)}>
			<PlusIcon />
			Add drain
		</Button>
	);

	return (
		<div className="flex flex-col gap-4">
			{items.length === 0 ? (
				<EmptyState
					illustration="radar"
					title="Ship your logs to the platform you already use"
					body="Add a drain to send your services' runtime logs to Loki, an OpenTelemetry collector, syslog or an HTTP endpoint such as Datadog, Better Stack or Axiom. Each server ships its own logs."
					action={add}
				/>
			) : (
				<>
					<div className="flex flex-wrap items-center justify-between gap-3">
						<p className="flex items-center gap-2 text-ink-3 text-xs">
							<SendIcon aria-hidden="true" className="size-4" />
							{own === 0
								? "No drain of your own yet."
								: `${own} ${own === 1 ? "drain" : "drains"} of your own.`}{" "}
							A server ships the logs it stores, so log capture has to be on for
							it.
						</p>
						{add}
					</div>
					<div className="grid gap-3 xl:grid-cols-2">
						{items.map((drain) => (
							<DrainCard
								key={drain.id}
								drain={drain}
								projectNames={projectNames}
								testing={testingId === drain.id}
								outcome={outcomes[drain.id]}
								onToggle={(enabled) => toggle.mutate({ drain, enabled })}
								onTest={() => test.mutate(drain)}
								onDismissOutcome={() => setOutcome(drain.id, undefined)}
								onEdit={() => setEditing(drain)}
								onRemove={() =>
									void confirm({
										title: `Delete ${drain.name}?`,
										body: "Every server stops shipping logs to it. Lines already delivered stay where they went.",
										cta: "Delete drain",
										danger: true,
										hold: false,
										onConfirm: async () => {
											await remove.mutateAsync(drain);
										},
									})
								}
							/>
						))}
					</div>
				</>
			)}
			<DrainDialog open={creating} onOpenChange={setCreating} drain={null} />
			<DrainDialog
				open={editing !== null}
				onOpenChange={(open) => (open ? null : setEditing(null))}
				drain={editing}
			/>
		</div>
	);
}
