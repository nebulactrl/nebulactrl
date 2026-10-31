import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { type FormEvent, useMemo, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { createService, deployService } from "#/api/gen/sdk.gen";
import { invalidateServices } from "#/api/invalidate";
import { type Environment, type Server, toService } from "#/api/models";
import { Alert, AlertDescription } from "#/components/ui/alert";
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
import { Field, FieldError, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Spinner } from "#/components/ui/spinner";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import { ServiceIcon } from "#/features/canvas/service-icon";
import { placeNode } from "#/features/canvas/use-layout-persistence";
import { ServerPicker } from "#/features/create/create-dialog";
import { openStagedReview } from "#/features/deploy-button";
import { isSchedulable } from "#/features/fleet/model";
import { ComposeEditor } from "./compose-editor";
import { checkBlocker } from "./model";
import { useStackCheck } from "./queries";
import { createStackActions } from "./store";

/**
 * The Compose stack dialog: name, a compose file with its live check, the
 * server and "Deploy now". Creating stages the stack; with Deploy now the
 * first deployment starts straight away (a production environment reviews it
 * first, like every production deploy).
 */
export function CreateStackDialog({
	open,
	position,
	environment,
	servers,
}: {
	open: boolean;
	position: { x: number; y: number } | undefined;
	environment: Pick<Environment, "id" | "name" | "isProduction">;
	servers: readonly Server[];
}) {
	return (
		<Dialog
			open={open}
			onOpenChange={(next) => !next && createStackActions.close()}
		>
			<DialogContent width={640}>
				{/* The popup unmounts after it closes, so each opening starts clean. */}
				<StackForm
					environment={environment}
					servers={servers}
					position={position}
				/>
			</DialogContent>
		</Dialog>
	);
}

function StackForm({
	environment,
	servers,
	position,
}: {
	environment: Pick<Environment, "id" | "name" | "isProduction">;
	servers: readonly Server[];
	position: { x: number; y: number } | undefined;
}) {
	const queryClient = useQueryClient();
	const usable = useMemo(
		() =>
			servers.filter(
				(server) => server.status !== "revoked" && server.status !== "pending",
			),
		[servers],
	);
	const [name, setName] = useState("");
	const [compose, setCompose] = useState("");
	const [deploy, setDeploy] = useState(false);
	const [serverId, setServerId] = useState(
		() =>
			usable.find(
				(server) => server.status === "online" && isSchedulable(server),
			)?.id ??
			usable.find(isSchedulable)?.id ??
			"",
	);
	// One key per opening: a retry after a lost response replays, not duplicates.
	const [idempotencyKey] = useState(() => crypto.randomUUID());
	const serverName = usable.find((server) => server.id === serverId)?.name;

	const check = useStackCheck({
		environmentId: environment.id,
		compose,
		serverId,
	});
	const blocker = checkBlocker(check);
	const trimmed = name.trim();
	const canCreate = trimmed !== "" && serverId !== "" && blocker === null;

	const create = useMutation({
		mutationFn: async () => {
			const { data } = await createService({
				path: { environmentId: environment.id },
				body: {
					name: trimmed,
					kind: "stack",
					sourceType: "compose",
					serverId,
					config: { stack: { compose } },
				},
				headers: { "Idempotency-Key": idempotencyKey },
			});
			return toService(data);
		},
		onSuccess: async (service) => {
			createStackActions.close();
			await invalidateServices(queryClient);
			if (position) {
				try {
					await placeNode(service.id, position);
				} catch {
					toast.warning(
						`${service.name} was placed automatically`,
						"Its position could not be saved where you dropped it.",
					);
				}
			}
			if (deploy && !environment.isProduction) {
				try {
					await deployService({
						path: { serviceId: service.id },
						headers: { "Idempotency-Key": crypto.randomUUID() },
					});
					toast.deployed(
						`Deploying ${service.name}`,
						"Follow it on its card or under Deployments.",
					);
					await invalidateServices(queryClient);
				} catch (failure) {
					toast.error(
						`Could not deploy ${service.name}`,
						errorMessage(failure),
					);
				}
				return;
			}
			toast.add({
				type: "success",
				title: `Created ${service.name}`,
				description: environment.isProduction
					? `Staged in ${environment.name}. Nothing runs until you review and deploy.`
					: "Nothing runs until it is deployed.",
				timeout: 9000,
				actionProps: {
					children: environment.isProduction ? "Review & deploy" : "Deploy now",
					onClick: () => {
						if (environment.isProduction) openStagedReview();
						else {
							void deployService({
								path: { serviceId: service.id },
								headers: { "Idempotency-Key": crypto.randomUUID() },
							})
								.then(() => invalidateServices(queryClient))
								.catch((failure) =>
									toast.error(
										`Could not deploy ${service.name}`,
										errorMessage(failure),
									),
								);
						}
					},
				},
			});
		},
	});

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (canCreate && !create.isPending) create.mutate();
	};

	return (
		<form onSubmit={submit} className="contents">
			<DialogHeader>
				<DialogTitle className="flex items-center gap-2.5">
					<ServiceIcon
						service={{ kind: "stack", sourceType: "compose" }}
						size={24}
					/>
					New Compose stack
				</DialogTitle>
				<DialogDescription>
					Runs a Docker Compose file on one server as a single service. Each
					Compose service becomes a component of the stack.
				</DialogDescription>
			</DialogHeader>
			<DialogBody className="overscroll-contain">
				<div className="flex flex-col gap-4">
					<Field>
						<FieldLabel htmlFor="stack-name">Name</FieldLabel>
						<Input
							id="stack-name"
							scale="lg"
							autoComplete="off"
							placeholder="my-stack"
							value={name}
							onChange={(event) => setName(event.target.value)}
						/>
					</Field>
					<ComposeEditor
						autoFocus
						value={compose}
						onChange={setCompose}
						state={check}
						serverName={serverName}
					/>
					<Field>
						<FieldLabel>Server</FieldLabel>
						{usable.length === 0 ? (
							<Alert variant="warning">
								<AlertDescription>
									No server is enrolled yet.{" "}
									<Link
										to="/servers"
										search={{ enroll: true }}
										className="text-accent-ink underline"
										onClick={() => createStackActions.close()}
									>
										Add a server
									</Link>{" "}
									first; a stack runs on one Docker host with the Compose
									plugin.
								</AlertDescription>
							</Alert>
						) : (
							<ServerPicker
								servers={usable}
								value={serverId}
								onChange={setServerId}
							/>
						)}
					</Field>
					<div className="flex items-start gap-3">
						<Switch
							id="stack-deploy"
							checked={deploy && !environment.isProduction}
							disabled={environment.isProduction}
							onCheckedChange={setDeploy}
						/>
						<div className="flex flex-col gap-0.5">
							<label
								htmlFor="stack-deploy"
								className="cursor-pointer font-medium text-[13px] text-ink-1"
							>
								Deploy now
							</label>
							<span className="text-[12px] text-ink-3 leading-[1.5]">
								{environment.isProduction
									? `${environment.name} is production: the stack is staged and you review it before it deploys.`
									: "Start the first deployment as soon as the stack is created. Otherwise it waits, staged."}
							</span>
						</div>
					</div>
				</div>
				{create.isError ? (
					<FieldError>{errorMessage(create.error)}</FieldError>
				) : null}
			</DialogBody>
			<DialogFooter>
				<span
					role="status"
					className="mr-auto min-w-0 text-[12px] text-ink-3 sm:max-w-[50%]"
				>
					{canCreate
						? null
						: trimmed === ""
							? "Give the stack a name."
							: blocker}
				</span>
				<Button
					type="button"
					variant="ghost"
					onClick={() => createStackActions.close()}
				>
					Cancel
				</Button>
				<Button type="submit" disabled={!canCreate || create.isPending}>
					{create.isPending ? <Spinner /> : null}
					Create stack
				</Button>
			</DialogFooter>
		</form>
	);
}
