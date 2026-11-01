import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { PlusIcon } from "lucide-react";
import { type FormEvent, useMemo, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { createDomain, deleteDomain } from "#/api/gen/sdk.gen";
import { pendingChangesQuery, serversQuery } from "#/api/hooks";
import type { Service, ServiceDomain } from "#/api/models";
import { useConfirm } from "#/components/common/confirm-dialog";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Spinner } from "#/components/ui/spinner";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import {
	type DnsProvider,
	isWildcardHost,
	resolverLabel,
	type ServerDns01,
	serverDns01Query,
} from "#/features/dns01/queries";
import {
	DomainProcessTarget,
	effectiveProcess,
} from "#/features/processes/domain-process-target";
import {
	domainProcessOptions,
	hasProcesses,
	storedDomainProcess,
} from "#/features/processes/model";
import { isAgentTooOld } from "#/features/proxy/lib/errors";
import { type RuleContext, wwwCounterpart } from "#/features/proxy/lib/rule";
import { domainStaged } from "#/features/proxy/lib/staged";
import { normalizeHostname } from "#/features/proxy/lib/validate";
import { invalidateRoutes } from "#/features/proxy/queries";
import { AgentTooOldBanner } from "#/features/proxy/ui/agent-too-old";
import { DomainTargetFields } from "#/features/stacks/domain-target";
import {
	domainBlocker,
	isStack,
	stackComponents,
} from "#/features/stacks/model";
import { DomainEditor } from "./domain-editor";
import { ZonePicker } from "./zone-picker";

/** The "no resolver" choice: the wildcard is covered by a certificate of the server. */
const NO_RESOLVER = "__covering__";

/** Type a hostname, add it: staged like every domain change. */
export function AddDomainForm({
	service,
	disabled,
	disabledReason,
	process,
	onProcessChange,
}: {
	service: Service;
	disabled: boolean;
	disabledReason?: string;
	/** The process picked for the new domain, when the parent also needs it (Generate domain). */
	process?: string;
	onProcessChange?(name: string): void;
}) {
	const queryClient = useQueryClient();
	const [hostname, setHostname] = useState("");
	const [resolver, setResolver] = useState("");
	const stack = isStack(service);
	const components = stackComponents(service);
	const [component, setComponent] = useState("");
	const [port, setPort] = useState("");
	const [wwwOn, setWwwOn] = useState(false);
	const processes = hasProcesses(service);
	const processOptions = domainProcessOptions(service);
	const [ownProcess, setOwnProcess] = useState("");
	const chosenProcess = effectiveProcess(processOptions, process ?? ownProcess);
	// A stack with one component needs no choice.
	const chosen =
		component !== ""
			? component
			: components.length === 1
				? (components[0]?.name ?? "")
				: "";
	const blocker = stack
		? domainBlocker({ hostname, component: chosen, port, components })
		: processes && chosenProcess === ""
			? processOptions.length === 0
				? "Give a process a port before adding a domain."
				: "Choose which process serves this domain."
			: null;
	const wildcard = isWildcardHost(hostname);
	const counterpart = wwwCounterpart(hostname);
	const www = wwwOn && counterpart !== null && !stack;
	const dnsState = useQuery({
		...serverDns01Query(service.serverId ?? ""),
		enabled: wildcard && service.serverId !== null,
	});
	const dns01 = dnsState.data as ServerDns01 | undefined;
	const add = useMutation({
		mutationFn: async () => {
			await createDomain({
				path: { serviceId: service.id },
				body: {
					hostname: normalizeHostname(hostname),
					...(wildcard && resolver !== ""
						? { dnsResolver: resolver as DnsProvider }
						: {}),
					...(www ? { wwwRedirect: true } : {}),
					...(stack
						? {
								component: chosen,
								...(port.trim() === "" ? {} : { targetPort: Number(port) }),
							}
						: {}),
					// The process' own port is the target; "" names the default process.
					...(processes
						? { process: storedDomainProcess(service, chosenProcess) }
						: {}),
				},
			});
		},
		onSuccess: async () => {
			setHostname("");
			setWwwOn(false);
			await invalidateRoutes(queryClient);
			toast.success("Domain added", "Staged until the next deploy.");
		},
	});
	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (hostname.trim() === "" || add.isPending || blocker !== null) return;
		add.mutate();
	};
	return (
		<div className="flex flex-col gap-2">
			{stack ? (
				<DomainTargetFields
					components={components}
					component={chosen}
					port={port}
					onComponent={setComponent}
					onPort={setPort}
					disabled={disabled}
				/>
			) : processes ? (
				<DomainProcessTarget
					options={processOptions}
					process={chosenProcess}
					onProcess={onProcessChange ?? setOwnProcess}
					disabled={disabled}
				/>
			) : null}
			<form onSubmit={submit} className="flex items-center gap-2">
				<Input
					aria-label="New domain"
					autoComplete="off"
					spellCheck={false}
					disabled={disabled}
					className="font-mono"
					placeholder="app.example.com or *.example.com"
					title={disabled ? disabledReason : undefined}
					value={hostname}
					onChange={(event) => setHostname(event.target.value)}
				/>
				{wildcard ? (
					<Select
						value={resolver === "" ? NO_RESOLVER : resolver}
						items={[
							{ value: NO_RESOLVER, label: "Covering wildcard certificate" },
							...(dns01?.resolvers ?? []).map((r) => ({
								value: r.provider,
								label: `DNS-01 · ${resolverLabel(r)}`,
							})),
						]}
						disabled={disabled}
						onValueChange={(next) => {
							if (next !== null) setResolver(next === NO_RESOLVER ? "" : next);
						}}
					>
						<SelectTrigger
							aria-label="Certificate resolver for the wildcard domain"
							className="shrink-0"
						>
							<SelectValue />
						</SelectTrigger>
						<SelectContent alignItemWithTrigger={false}>
							<SelectItem value={NO_RESOLVER}>
								Covering wildcard certificate
							</SelectItem>
							{(dns01?.resolvers ?? []).map((r) => (
								<SelectItem key={r.provider} value={r.provider}>
									DNS-01 · {resolverLabel(r)}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
				) : null}
				<Button
					type="submit"
					size="md"
					variant="secondary"
					disabled={
						disabled ||
						hostname.trim() === "" ||
						add.isPending ||
						blocker !== null
					}
					title={blocker ?? undefined}
				>
					{add.isPending ? <Spinner /> : <PlusIcon aria-hidden />}
					Add domain
				</Button>
			</form>
			{counterpart !== null && !stack ? (
				<div className="flex items-center gap-2 text-ink-2 text-sm">
					<Switch
						id="add-domain-www"
						size="sm"
						checked={www}
						disabled={disabled}
						onCheckedChange={setWwwOn}
					/>
					<label htmlFor="add-domain-www" className="cursor-pointer">
						Also route{" "}
						<span className="font-mono text-[13px]">{counterpart}</span> and
						redirect it here (301)
					</label>
				</div>
			) : null}
			<ZonePicker
				hostname={hostname}
				disabled={disabled}
				onPick={setHostname}
			/>
			{isAgentTooOld(add.error) ? (
				<AgentTooOldBanner
					{...(service.server ? { serverName: service.server.name } : {})}
				/>
			) : add.error ? (
				<FieldError>{errorMessage(add.error)}</FieldError>
			) : null}
		</div>
	);
}

/** The domains of a service, each an editor card with its staged state and DNS guidance. */
export function DomainList({ service }: { service: Service }) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const servers = useQuery(serversQuery());
	const server = servers.data?.find(
		(candidate) => candidate.id === service.serverId,
	);
	const publicIp = server?.publicIp ?? null;
	const agentTooOld =
		server !== undefined && server.status === "online" && !server.proxy.v2;
	const pending = useQuery({
		...pendingChangesQuery(service.id),
		enabled: service.hasStagedChanges,
	});
	const changes = pending.data ?? [];

	const remove = useMutation({
		mutationFn: async (domainId: string) => {
			await deleteDomain({ path: { domainId } });
		},
		onSuccess: () => invalidateRoutes(queryClient),
	});

	const removeDomain = async (domain: ServiceDomain) => {
		await confirm({
			title: `Remove ${domain.hostname}${domain.pathPrefix}?`,
			body: "The route is removed on the next deploy. The service keeps running on its other domains and its private host.",
			cta: "Remove domain",
			glyph: "trash",
			danger: true,
			hold: false,
			onConfirm: () => remove.mutateAsync(domain.id),
		});
	};

	const context: RuleContext = useMemo(
		() => ({
			otherDomains: service.domains.map((d) => ({
				hostname: d.hostname,
				pathPrefix: d.pathPrefix,
			})),
			redirects: service.domains
				.filter((d) => d.kind === "redirect" && d.pathPrefix === "")
				.map((d) => ({ from: d.hostname, to: d.redirectTo })),
		}),
		[service.domains],
	);

	return (
		<div className="flex flex-col gap-2">
			{agentTooOld ? (
				<AgentTooOldBanner {...(server ? { serverName: server.name } : {})} />
			) : null}
			{service.domains.length > 0 ? (
				<ul aria-label="Domains" className="flex flex-col gap-2">
					{service.domains.map((domain) => (
						<DomainEditor
							key={domain.id}
							service={service}
							domain={domain}
							publicIp={publicIp}
							staged={domainStaged(changes, domain.hostname, domain.pathPrefix)}
							context={context}
							agentTooOld={agentTooOld}
							removing={remove.isPending}
							onRemove={() => void removeDomain(domain)}
						/>
					))}
				</ul>
			) : null}
		</div>
	);
}
