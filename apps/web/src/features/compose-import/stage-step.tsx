import { Link } from "@tanstack/react-router";
import { useId } from "react";
import type { ComposePlan } from "#/api/gen/types.gen";
import type { Server } from "#/api/models";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { Field, FieldDescription, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { ServerPicker } from "#/features/create/create-dialog";
import { PlanChecks } from "#/features/templates/plan-checks";
import { type ImportFailure, summarizeChecks } from "./lib";

/**
 * Step three: which server takes the services, what the group is called, and
 * the preflight against that server. The buttons live in the dialog footer.
 */
export function StageStep({
	plan,
	servers,
	serverId,
	onServer,
	title,
	onTitle,
	includedNames,
	stackName,
	checking,
	failure,
	onCheckAgain,
}: {
	plan: ComposePlan;
	servers: readonly Server[];
	serverId: string;
	onServer(id: string): void;
	title: string;
	onTitle(next: string): void;
	includedNames: readonly string[];
	/** Set for a stack import: the plan's stack name, which the title field then names. */
	stackName?: string | undefined;
	checking: boolean;
	failure: ImportFailure | null;
	onCheckAgain(): void;
}) {
	const id = useId();
	const checks = summarizeChecks(plan.checks);
	return (
		<div className="flex flex-col gap-6">
			<Field>
				<FieldLabel htmlFor={`${id}-title`}>
					{stackName === undefined ? "Group title" : "Stack name"}
				</FieldLabel>
				<Input
					id={`${id}-title`}
					autoComplete="off"
					placeholder={stackName ?? plan.name}
					value={title}
					onChange={(event) => onTitle(event.target.value)}
					className="max-w-sm"
				/>
				<FieldDescription>
					{stackName === undefined
						? "The frame around the new services on the canvas."
						: "The stack is one node on the canvas, with no frame around it."}
				</FieldDescription>
			</Field>
			<section aria-label="Server" className="flex flex-col gap-2">
				<h3 className="type-label m-0">Server</h3>
				{servers.length === 0 ? (
					<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
						No server is enrolled yet.{" "}
						<Link
							to="/servers"
							className="text-accent-ink underline-offset-2 hover:underline"
						>
							Add a server
						</Link>{" "}
						first.
					</p>
				) : (
					<ServerPicker
						servers={servers}
						value={serverId}
						onChange={onServer}
					/>
				)}
			</section>
			<PlanChecks summary={checks} checking={checking} waiting={null} />
			<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
				{includedNames.length > 0 ? (
					<>
						Stages{" "}
						<span className="font-mono text-ink-2">
							{includedNames.join(", ")}
						</span>
						. Nothing runs until you deploy.
					</>
				) : (
					"No service is included."
				)}
			</p>
			{failure ? (
				<Alert variant="destructive">
					<AlertTitle>{failure.title}</AlertTitle>
					<AlertDescription>
						<p>{failure.body}</p>
						{failure.replan ? (
							<Button variant="secondary" size="xs" onClick={onCheckAgain}>
								Check again
							</Button>
						) : null}
					</AlertDescription>
				</Alert>
			) : null}
		</div>
	);
}
