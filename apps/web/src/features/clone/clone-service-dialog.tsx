import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useEffect, useId, useMemo, useRef, useState } from "react";
import { errorCode, errorMessage } from "#/api/client-config";
import { cloneService } from "#/api/gen/sdk.gen";
import { environmentsQuery } from "#/api/hooks";
import { invalidateServices } from "#/api/invalidate";
import type { Service } from "#/api/models";
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
import {
	Field,
	FieldDescription,
	FieldError,
	FieldLabel,
} from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { useServers } from "#/features/canvas/data";
import { DomainsField } from "./domains-field";
import {
	type ClonePlan,
	conflictHosts,
	type DomainMode,
	defaultSuffix,
	normalizeSuffix,
	toClonePlan,
} from "./model";
import { EnvironmentSelect, SAME_SERVER, ServerChoice } from "./pickers";
import { ClonePlanView } from "./plan-view";

/** What the server said, in words a person can act on. */
function cloneErrorText(error: unknown): string {
	switch (errorCode(error)) {
		case "domain_conflict":
			return "A hostname of the clone is already in use. Choose another suffix, or clone without domains.";
		case "not_applicable":
			return errorMessage(error);
		default:
			return errorMessage(error);
	}
}

/**
 * "Clone <service>": a staged copy in this or another environment. Secrets are
 * copied sealed and never shown; generated values are generated again; volumes
 * and databases start empty. Check runs the same request as a dry run and
 * lists what would happen, conflicts and unresolved references included.
 */
export function CloneServiceDialog({
	service,
	open,
	onOpenChange,
}: {
	service: Service;
	open: boolean;
	onOpenChange: (open: boolean) => void;
}) {
	const queryClient = useQueryClient();
	const navigate = useNavigate();
	const ids = useId();
	const idempotencyKey = useRef(crypto.randomUUID());
	const environments = useQuery({
		...environmentsQuery(service.projectId),
		enabled: open,
	});
	const servers = useServers();
	const [name, setName] = useState("");
	const [environmentId, setEnvironmentId] = useState(service.environmentId);
	const [server, setServer] = useState(SAME_SERVER);
	const [mode, setMode] = useState<DomainMode>("drop");
	const [suffix, setSuffix] = useState("");
	const [plan, setPlan] = useState<ClonePlan | null>(null);

	useEffect(() => {
		if (!open) return;
		idempotencyKey.current = crypto.randomUUID();
		setName("");
		setEnvironmentId(service.environmentId);
		setServer(SAME_SERVER);
		setMode("drop");
		setSuffix("");
		setPlan(null);
	}, [open, service.environmentId]);

	const sameEnvironment = environmentId === service.environmentId;
	const target = environments.data?.find((e) => e.id === environmentId);
	const placeholder = defaultSuffix(sameEnvironment, target?.slug ?? "");
	const firstDomain = service.domains.find((domain) => domain.kind === "serve");

	const body = useMemo(() => {
		const domainSuffix = normalizeSuffix(suffix);
		return {
			...(name.trim() !== "" ? { name: name.trim() } : {}),
			...(sameEnvironment ? {} : { environmentId }),
			...(server !== SAME_SERVER ? { serverId: server } : {}),
			domains: mode,
			...(mode === "suffix" && domainSuffix !== "" ? { domainSuffix } : {}),
		};
	}, [name, environmentId, sameEnvironment, server, mode, suffix]);

	// A plan describes one set of choices: changing any of them clears it.
	// A new set of choices is a new request: it must not replay an earlier answer.
	// biome-ignore lint/correctness/useExhaustiveDependencies: body is the dependency; the effect only resets.
	useEffect(() => {
		setPlan(null);
		idempotencyKey.current = crypto.randomUUID();
	}, [body]);

	const check = useMutation({
		mutationFn: async () => {
			const { data } = await cloneService({
				path: { serviceId: service.id },
				body: { ...body, dryRun: true },
			});
			return toClonePlan(data);
		},
		onSuccess: setPlan,
	});

	const clone = useMutation({
		mutationFn: async () => {
			const { data } = await cloneService({
				path: { serviceId: service.id },
				body,
				headers: { "Idempotency-Key": idempotencyKey.current },
			});
			return data;
		},
		onSuccess: async (result) => {
			await invalidateServices(queryClient);
			onOpenChange(false);
			const created = result.service;
			toast.success(
				`Cloned ${service.name}`,
				"Staged: deploy the clone to start it.",
			);
			if (created && target) {
				void navigate({
					to: "/p/$projectId/$envSlug",
					params: { projectId: service.projectId, envSlug: target.slug },
					search: { service: created.id },
				});
			}
		},
	});

	const blocked = plan !== null && conflictHosts(plan).length > 0;
	const busy = check.isPending || clone.isPending;
	const error = clone.error ?? check.error;

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="md" showCloseButton>
				<DialogHeader>
					<DialogTitle>Clone {service.name}</DialogTitle>
					<DialogDescription>
						A staged copy of the configuration and variables. Secrets are copied
						without being shown, and generated ones are generated again. Volumes
						and databases start empty. Nothing runs until you deploy it.
					</DialogDescription>
				</DialogHeader>
				<DialogBody>
					<Field>
						<FieldLabel htmlFor={`${ids}-name`}>Name</FieldLabel>
						<Input
							id={`${ids}-name`}
							autoFocus
							autoComplete="off"
							placeholder={
								sameEnvironment ? `${service.name} copy` : service.name
							}
							value={name}
							onChange={(event) => setName(event.target.value)}
						/>
					</Field>
					<Field>
						<FieldLabel htmlFor={`${ids}-env`}>Environment</FieldLabel>
						<EnvironmentSelect
							id={`${ids}-env`}
							environments={environments.data ?? []}
							value={environmentId}
							onChange={setEnvironmentId}
						/>
						{sameEnvironment ? null : (
							<FieldDescription>
								Links to other services are not copied, and variable references
								are checked in {target?.name ?? "that environment"}.
							</FieldDescription>
						)}
					</Field>
					<Field>
						<FieldLabel htmlFor={`${ids}-server`}>Server</FieldLabel>
						<ServerChoice
							id={`${ids}-server`}
							servers={servers.data ?? []}
							value={server}
							onChange={setServer}
							sameLabel={`Same as ${service.name}`}
						/>
					</Field>
					<DomainsField
						mode={mode}
						onMode={setMode}
						suffix={suffix}
						onSuffix={setSuffix}
						placeholder={placeholder}
						example={firstDomain?.hostname}
					/>
					{plan ? <ClonePlanView plan={plan} showServices={false} /> : null}
					{error ? <FieldError>{cloneErrorText(error)}</FieldError> : null}
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
						disabled={busy || environmentId === ""}
						onClick={() => check.mutate()}
					>
						{check.isPending ? <Spinner /> : null}
						Check
					</Button>
					<Button
						type="button"
						variant="primary"
						disabled={busy || blocked || environmentId === ""}
						onClick={() => clone.mutate()}
					>
						{clone.isPending ? <Spinner /> : null}
						Clone service
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
