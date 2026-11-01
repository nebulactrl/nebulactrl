import { useQuery } from "@tanstack/react-query";
import { errorMessage } from "#/api/client-config";
import { rotateVariable } from "#/api/gen/sdk.gen";
import type { VariableRotationPlan } from "#/api/gen/types.gen";
import { Alert, AlertDescription } from "#/components/ui/alert";
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
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { useVariableMutations } from "./use-variable-mutations";

/** What a rotation dialog needs from its owner (a service, or a shared layer). */
export interface RotationSource {
	/** Identifies the plan query. */
	queryKey: readonly unknown[];
	/** The dry-run plan. */
	fetchPlan(): Promise<VariableRotationPlan>;
	/** Stages the rotation; resolves to the applied plan. */
	stage: {
		mutate(
			key: string,
			options: { onSuccess(result: VariableRotationPlan | undefined): void },
		): void;
		isPending: boolean;
		isError: boolean;
		error: unknown;
	};
}

/**
 * Rotation as a plan, never a surprise: a dry run lists the services that
 * consume the variable, the order to deploy them in and what could go wrong;
 * "Stage rotation" only stages the new value. Deploying stays a separate,
 * deliberate step.
 */
export function RotatePlanDialog({
	source,
	variableKey,
	onClose,
}: {
	source: RotationSource;
	/** The variable to rotate; null when closed. */
	variableKey: string | null;
	onClose(): void;
}) {
	return (
		<Dialog open={variableKey !== null} onOpenChange={(o) => !o && onClose()}>
			<DialogContent>
				{variableKey !== null ? (
					<RotateBody
						source={source}
						variableKey={variableKey}
						onClose={onClose}
					/>
				) : null}
			</DialogContent>
		</Dialog>
	);
}

/** Rotation of a service's generated variable. */
export function RotateDialog({
	serviceId,
	variableKey,
	onClose,
}: {
	serviceId: string;
	variableKey: string | null;
	onClose(): void;
}) {
	const { rotate } = useVariableMutations(serviceId);
	const source: RotationSource = {
		queryKey: ["rotation-plan", serviceId, variableKey],
		fetchPlan: async () => {
			const { data } = await rotateVariable({
				path: { serviceId, key: variableKey ?? "" },
				body: { dryRun: true },
			});
			return data;
		},
		stage: rotate,
	};
	return (
		<RotatePlanDialog
			source={source}
			variableKey={variableKey}
			onClose={onClose}
		/>
	);
}

function RotateBody({
	source,
	variableKey,
	onClose,
}: {
	source: RotationSource;
	variableKey: string;
	onClose(): void;
}) {
	const rotate = source.stage;
	const plan = useQuery({
		queryKey: source.queryKey,
		queryFn: source.fetchPlan,
		staleTime: 0,
		gcTime: 0,
		retry: false,
	});
	const blockers = plan.data?.blockers ?? [];
	const affected = plan.data?.affected ?? [];
	const stage = () =>
		rotate.mutate(variableKey, {
			onSuccess: (result) => {
				const order = (result?.deployOrder ?? []).join(" → ");
				toast.success(
					`Rotated ${variableKey}`,
					order ? `Staged. Deploy in this order: ${order}.` : "Staged.",
				);
				onClose();
			},
		});

	return (
		<>
			<DialogHeader>
				<DialogTitle>Rotate {variableKey}</DialogTitle>
				<DialogDescription>
					Generates a new value and stages it. Nothing is deployed until you
					deploy the staged changes.
				</DialogDescription>
			</DialogHeader>
			<DialogBody>
				{plan.isPending ? (
					<div
						role="status"
						aria-label="Planning the rotation"
						className="flex flex-col gap-2"
					>
						<Skeleton className="h-8 w-full" />
						<Skeleton className="h-8 w-full" />
					</div>
				) : plan.isError ? (
					<Alert variant="destructive">{errorMessage(plan.error)}</Alert>
				) : (
					<div className="flex flex-col gap-3">
						{blockers.map((blocker) => (
							<Alert key={blocker.code} variant="destructive">
								{blocker.message}
							</Alert>
						))}
						<table className="w-full text-left text-[12.5px]">
							<thead className="text-ink-3">
								<tr>
									<th className="py-1 font-medium">Service</th>
									<th className="py-1 font-medium">Changes</th>
									<th className="py-1 font-medium">Deploy</th>
								</tr>
							</thead>
							<tbody className="divide-y divide-line-faint">
								{affected.map((service) => (
									<tr key={service.serviceId}>
										<td className="py-1.5 font-medium font-mono">
											{service.slug}
										</td>
										<td className="py-1.5 font-mono text-ink-3">
											{(service.via ?? []).join(", ")}
										</td>
										<td className="py-1.5">
											<Badge tone={service.downtime === "none" ? "ok" : "warn"}>
												{service.strategy}
												{service.downtime === "none"
													? ", no downtime"
													: ", brief downtime"}
											</Badge>
										</td>
									</tr>
								))}
							</tbody>
						</table>
						{(plan.data?.deployOrder ?? []).length > 1 ? (
							<p className="type-secondary">
								Deploy order:{" "}
								<span className="font-mono text-ink-2">
									{(plan.data?.deployOrder ?? []).join(" → ")}
								</span>
							</p>
						) : null}
						{(plan.data?.warnings ?? []).map((warning) => (
							<Alert key={warning} variant="warning">
								<AlertDescription>{warning}</AlertDescription>
							</Alert>
						))}
					</div>
				)}
				{rotate.isError ? (
					<Alert variant="destructive">{errorMessage(rotate.error)}</Alert>
				) : null}
			</DialogBody>
			<DialogFooter>
				<Button type="button" variant="ghost" onClick={onClose}>
					Cancel
				</Button>
				<Button
					type="button"
					disabled={
						plan.isPending ||
						plan.isError ||
						blockers.length > 0 ||
						rotate.isPending
					}
					onClick={stage}
				>
					{rotate.isPending ? <Spinner /> : null}
					Stage rotation
				</Button>
			</DialogFooter>
		</>
	);
}
