import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useEffect, useId, useMemo, useRef, useState } from "react";
import { errorCode, errorMessage } from "#/api/client-config";
import { cloneEnvironment } from "#/api/gen/sdk.gen";
import { environmentsQuery } from "#/api/hooks";
import { invalidateServices } from "#/api/invalidate";
import type { Environment } from "#/api/models";
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
import { toast } from "#/components/ui/toast";
import { useServers } from "#/features/canvas/data";
import { DomainsField } from "./domains-field";
import {
	type ClonePlan,
	conflictHosts,
	type DomainMode,
	normalizeSuffix,
	toClonePlan,
} from "./model";
import { SAME_SERVER, ServerChoice } from "./pickers";
import { ClonePlanView } from "./plan-view";

/**
 * "Clone environment": a new, non-production environment with a staged copy
 * of every service of this one. Slugs are kept, so references such as
 * `${{postgres.DATABASE_URL}}` point at the cloned siblings. Databases and
 * volumes start empty.
 */
export function CloneEnvironmentDialog({
	environment,
	open,
	onOpenChange,
}: {
	environment: Environment;
	open: boolean;
	onOpenChange: (open: boolean) => void;
}) {
	const queryClient = useQueryClient();
	const navigate = useNavigate();
	const ids = useId();
	const idempotencyKey = useRef(crypto.randomUUID());
	const servers = useServers();
	const [name, setName] = useState("");
	const [server, setServer] = useState(SAME_SERVER);
	const [mode, setMode] = useState<DomainMode>("drop");
	const [suffix, setSuffix] = useState("");
	const [plan, setPlan] = useState<ClonePlan | null>(null);

	useEffect(() => {
		if (!open) return;
		idempotencyKey.current = crypto.randomUUID();
		setName("");
		setServer(SAME_SERVER);
		setMode("drop");
		setSuffix("");
		setPlan(null);
	}, [open]);

	const body = useMemo(() => {
		const domainSuffix = normalizeSuffix(suffix);
		return {
			name: name.trim(),
			...(server !== SAME_SERVER ? { serverId: server } : {}),
			domains: mode,
			...(mode === "suffix" && domainSuffix !== "" ? { domainSuffix } : {}),
		};
	}, [name, server, mode, suffix]);

	// A plan describes one set of choices: changing any of them clears it.
	// A new set of choices is a new request: it must not replay an earlier answer.
	// biome-ignore lint/correctness/useExhaustiveDependencies: body is the dependency; the effect only resets.
	useEffect(() => {
		setPlan(null);
		idempotencyKey.current = crypto.randomUUID();
	}, [body]);

	const check = useMutation({
		mutationFn: async () => {
			const { data } = await cloneEnvironment({
				path: { environmentId: environment.id },
				body: { ...body, dryRun: true },
			});
			return toClonePlan(data);
		},
		onSuccess: setPlan,
	});

	const clone = useMutation({
		mutationFn: async () => {
			const { data } = await cloneEnvironment({
				path: { environmentId: environment.id },
				body,
				headers: { "Idempotency-Key": idempotencyKey.current },
			});
			return data;
		},
		onSuccess: async (result) => {
			await Promise.all([
				queryClient.invalidateQueries({
					queryKey: environmentsQuery(environment.projectId).queryKey,
				}),
				invalidateServices(queryClient),
			]);
			onOpenChange(false);
			const created = result.environment;
			toast.success(
				`Cloned ${environment.name}`,
				"Staged: deploy its services to start them.",
			);
			if (created) {
				void navigate({
					to: "/p/$projectId/$envSlug",
					params: { projectId: environment.projectId, envSlug: created.slug },
				});
			}
		},
	});

	const blocked = plan !== null && conflictHosts(plan).length > 0;
	const busy = check.isPending || clone.isPending;
	const error = clone.error ?? check.error;
	const empty = name.trim() === "";

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="md" showCloseButton>
				<DialogHeader>
					<DialogTitle>Clone {environment.name}</DialogTitle>
					<DialogDescription>
						A new environment with a staged copy of every service: settings,
						variables, volumes and links. Secrets are copied without being shown
						and generated ones are generated again. Databases and volumes start
						empty. It is never a production environment.
					</DialogDescription>
				</DialogHeader>
				<DialogBody>
					<Field>
						<FieldLabel htmlFor={`${ids}-name`}>Name</FieldLabel>
						<Input
							id={`${ids}-name`}
							scale="lg"
							autoFocus
							autoComplete="off"
							placeholder="staging"
							value={name}
							onChange={(event) => setName(event.target.value)}
						/>
					</Field>
					<Field>
						<FieldLabel htmlFor={`${ids}-server`}>Server</FieldLabel>
						<ServerChoice
							id={`${ids}-server`}
							servers={servers.data ?? []}
							value={server}
							onChange={setServer}
							sameLabel="Each service keeps its server"
						/>
					</Field>
					<DomainsField
						mode={mode}
						onMode={setMode}
						suffix={suffix}
						onSuffix={setSuffix}
						placeholder="-<name>"
					/>
					{plan ? <ClonePlanView plan={plan} showServices /> : null}
					{error ? (
						<FieldError>
							{errorCode(error) === "domain_conflict"
								? "A hostname of the clone is already in use. Choose another suffix, or clone without domains."
								: errorMessage(error)}
						</FieldError>
					) : null}
				</DialogBody>
				<DialogFooter>
					<Button
						type="button"
						variant="secondary"
						onClick={() => onOpenChange(false)}
					>
						Cancel
					</Button>
					<Button
						type="button"
						variant="secondary"
						disabled={busy || empty}
						onClick={() => check.mutate()}
					>
						{check.isPending ? <Spinner /> : null}
						Check
					</Button>
					<Button
						type="button"
						variant="primary"
						disabled={busy || empty || blocked}
						onClick={() => clone.mutate()}
					>
						{clone.isPending ? <Spinner /> : null}
						Clone environment
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
