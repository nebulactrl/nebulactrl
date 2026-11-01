import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { rotateDatabaseCredentialsMutation } from "#/api/gen/@tanstack/react-query.gen";
import type { RotationResult } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
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
import { openStagedReview } from "#/features/deploy-button";
import { canRotate, ROTATION_CONFIRM, ROTATION_MODE_TEXT } from "./model";
import {
	DATABASE_POLL_MS,
	databaseQuery,
	invalidateDatabaseAndServices,
} from "./queries";

export interface RotateDialogProps {
	service: Pick<Service, "id" | "name">;
	open: boolean;
	onOpenChange(open: boolean): void;
}

/**
 * Rotate credentials: a dry run first (what changes, who is staged, what
 * breaks), then the real call after confirmation. A `pending` rotation is the
 * agent changing the password; the database view is polled until it says
 * applied or failed. Nothing is deployed: the services it staged show up in
 * the staged-changes review.
 */
export function RotateDialog({
	service,
	open,
	onOpenChange,
}: RotateDialogProps) {
	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="md">
				<RotateBody service={service} onClose={() => onOpenChange(false)} />
			</DialogContent>
		</Dialog>
	);
}

function List({ items }: { items: readonly string[] }) {
	return (
		<ul className="flex list-disc flex-col gap-1 pl-4">
			{items.map((item) => (
				<li key={item}>{item}</li>
			))}
		</ul>
	);
}

function RotateBody({
	service,
	onClose,
}: {
	service: Pick<Service, "id" | "name">;
	onClose(): void;
}) {
	const queryClient = useQueryClient();
	const plan = useMutation(rotateDatabaseCredentialsMutation());
	const apply = useMutation(rotateDatabaseCredentialsMutation());
	const [applied, setApplied] = useState<RotationResult | null>(null);

	// The dry run happens once, when the dialog opens.
	const planned = useRef(false);
	useEffect(() => {
		if (planned.current) return;
		planned.current = true;
		plan.mutate({ path: { serviceId: service.id }, body: { dryRun: true } });
	}, [plan.mutate, service.id]);

	// While the agent works, the view is polled; its `rotation` says how it ended.
	const view = useQuery({
		...databaseQuery(service.id),
		enabled: applied?.status === "pending",
		refetchInterval: DATABASE_POLL_MS,
	});
	const rotation = view.data?.rotation;
	const outcome =
		applied?.status === "pending" &&
		rotation !== undefined &&
		(applied.rotationId === undefined || rotation.id === applied.rotationId)
			? rotation.status
			: null;

	useEffect(() => {
		if (outcome === "applied" || outcome === "failed") {
			void invalidateDatabaseAndServices(queryClient);
		}
	}, [outcome, queryClient]);

	const result = plan.data?.plan;
	const blockers = result?.blockers ?? [];
	const warnings = result?.warnings ?? [];
	const consumers = result?.consumers ?? [];

	const confirm = () =>
		apply.mutate(
			{ path: { serviceId: service.id }, body: { dryRun: false } },
			{
				onSuccess: (data) => {
					setApplied(data);
					void invalidateDatabaseAndServices(queryClient);
				},
			},
		);

	const done =
		applied !== null && (applied.status === "applied" || outcome === "applied");
	const failed = outcome === "failed";
	const working = applied?.status === "pending" && !done && !failed;

	return (
		<>
			<DialogHeader>
				<DialogTitle>Rotate credentials · {service.name}</DialogTitle>
				<DialogDescription>
					{result
						? ROTATION_MODE_TEXT[result.mode]
						: "Generates a new password for this database."}
				</DialogDescription>
			</DialogHeader>
			<DialogBody>
				{plan.isPending ? (
					<div
						className="flex flex-col gap-2"
						role="status"
						aria-label="Planning the rotation"
					>
						<Skeleton className="h-5 w-2/3" />
						<Skeleton className="h-5 w-1/2" />
						<Skeleton className="h-16 w-full" />
					</div>
				) : null}
				{plan.isError ? (
					<Alert variant="destructive">
						<AlertTitle>The rotation could not be planned</AlertTitle>
						<AlertDescription>{errorMessage(plan.error)}</AlertDescription>
					</Alert>
				) : null}

				{result && !applied ? (
					<>
						<section
							aria-label="Services that will be staged"
							className="flex flex-col gap-1.5"
						>
							<h3 className="type-label">Services that will be staged</h3>
							{consumers.length === 0 ? (
								<p className="type-secondary">
									No other service references this database.
								</p>
							) : (
								<ul className="rule-rows">
									{consumers.map((consumer) => (
										<li
											key={consumer.serviceId}
											className="flex items-baseline gap-2 py-1.5 text-[13px]"
										>
											<span className="text-ink-1">{consumer.name}</span>
											<span className="font-mono text-2xs text-ink-3">
												{consumer.slug}
											</span>
										</li>
									))}
								</ul>
							)}
							<p className="type-secondary">
								They are staged, not deployed: the staged changes review lists
								them.
								{result.databaseRestart
									? " The database itself restarts when you deploy."
									: ""}
							</p>
						</section>
						{warnings.length > 0 ? (
							<Alert variant="warning">
								<AlertTitle>Before you rotate</AlertTitle>
								<AlertDescription>
									<List items={warnings} />
								</AlertDescription>
							</Alert>
						) : null}
						{blockers.length > 0 ? (
							<Alert variant="destructive">
								<AlertTitle>It cannot be rotated right now</AlertTitle>
								<AlertDescription>
									<List items={blockers} />
								</AlertDescription>
							</Alert>
						) : null}
						{apply.isError ? (
							<Alert variant="destructive">
								<AlertTitle>The rotation was refused</AlertTitle>
								<AlertDescription>{errorMessage(apply.error)}</AlertDescription>
							</Alert>
						) : null}
					</>
				) : null}

				{working ? (
					<div
						className="flex items-center gap-2.5 text-[13px] text-ink-2"
						role="status"
					>
						<Spinner />
						Rotating: the agent is changing the password. This usually takes a
						few seconds.
					</div>
				) : null}
				{failed ? (
					<Alert variant="destructive">
						<AlertTitle>The rotation failed</AlertTitle>
						<AlertDescription>
							{rotation?.error ?? "The agent did not report a reason."} The
							password was not changed.
						</AlertDescription>
					</Alert>
				) : null}
				{done ? (
					<Alert variant="success">
						<AlertTitle>Credentials rotated</AlertTitle>
						<AlertDescription>
							Deploy the staged changes so the services that reference this
							database pick up the new password.
						</AlertDescription>
					</Alert>
				) : null}
			</DialogBody>
			<DialogFooter>
				{done ? (
					<>
						<Button variant="ghost" onClick={onClose}>
							Close
						</Button>
						<Button
							onClick={() => {
								onClose();
								openStagedReview();
							}}
						>
							Review staged changes
						</Button>
					</>
				) : (
					<>
						<Button variant="ghost" onClick={onClose} disabled={working}>
							{failed ? "Close" : "Cancel"}
						</Button>
						{failed ? null : (
							<Button
								disabled={
									!canRotate(result) || apply.isPending || applied !== null
								}
								onClick={confirm}
							>
								{apply.isPending || working ? <Spinner /> : null}
								{result ? ROTATION_CONFIRM[result.mode] : "Rotate password"}
							</Button>
						)}
					</>
				)}
			</DialogFooter>
		</>
	);
}
