import { useQuery } from "@tanstack/react-query";
import { type FormEvent, useMemo, useState } from "react";
import { errorMessage } from "#/api/client-config";
import type { CreateRouteInputBody } from "#/api/gen/types.gen";
import { servicesQuery } from "#/api/hooks";
import type { Environment, Server, Service } from "#/api/models";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { CheckLabel } from "#/components/ui/check-label";
import {
	DialogBody,
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
import { Segmented } from "#/components/ui/segmented";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Spinner } from "#/components/ui/spinner";
import { AccessRulesEditor } from "#/features/access-rules/access-rules-editor";
import { checkRules, cleanList, rulesOk } from "#/features/access-rules/rules";
import { type RouteCreateRequest, routeCreate } from "../create-store";
import { fieldError, isAgentTooOld } from "../lib/errors";
import {
	checkPort,
	FIREWALL_NOTE,
	type L4Protocol,
	l4PlanLines,
	RESTART_WARNING,
	UDP_WARNING,
} from "../lib/l4";
import { withHost } from "../lib/tailscale";
import { validateListenPort, validateUpstreamAddress } from "../lib/validate";
import { serverPortsQuery } from "../queries";
import { AgentTooOldBanner } from "../ui/agent-too-old";
import { TailnetDevicePicker } from "../ui/tailnet-device-picker";
import { routeServers, ServerSelect } from "./server-select";
import { useCreateRoute } from "./use-create-route";

type Source = "service" | "address";

/** The container port a managed service listens on, when it says. */
function servicePort(service: Service): number | undefined {
	const port = service.config.port;
	return port !== undefined && port > 0 ? port : undefined;
}

/**
 * "TCP/UDP port": open a port on a server's proxy and forward it to a managed
 * service or to an address. The port is checked against the server's ports as
 * it is typed; a new port restarts the proxy once, and says so.
 */
export function PortForm({
	environment,
	servers,
	request,
}: {
	environment: Pick<Environment, "id" | "name" | "isProduction">;
	servers: readonly Server[];
	request: RouteCreateRequest;
}) {
	const usable = routeServers(servers);
	const services = useQuery(servicesQuery(environment.id));
	const candidates = useMemo(
		() =>
			(services.data ?? []).filter(
				(service) => service.kind !== "route" && service.serverId !== null,
			),
		[services.data],
	);
	const target = request.target
		? candidates.find((service) => service.id === request.target?.serviceId)
		: undefined;

	const [protocol, setProtocol] = useState<L4Protocol>("tcp");
	const [source, setSource] = useState<Source>(
		request.target ? "service" : "service",
	);
	const [serviceId, setServiceId] = useState(request.target?.serviceId ?? "");
	const [targetPort, setTargetPort] = useState(
		request.target?.port ? String(request.target.port) : "",
	);
	const [addressServerId, setAddressServerId] = useState(
		() => usable.find((s) => s.proxy.v2 && s.status === "online")?.id ?? "",
	);
	const [address, setAddress] = useState("");
	const [listenPort, setListenPort] = useState("");
	const [cidrs, setCidrs] = useState<string[]>([]);
	const [denyCidrs, setDenyCidrs] = useState<string[]>([]);
	const [openToInternet, setOpenToInternet] = useState(false);
	const [name, setName] = useState("");
	const [touched, setTouched] = useState(false);
	const create = useCreateRoute(environment, request.position);

	const chosen = candidates.find((service) => service.id === serviceId);
	const serverId =
		source === "service" ? (chosen?.serverId ?? "") : addressServerId;
	const server = servers.find((candidate) => candidate.id === serverId);

	const ports = useQuery({
		...serverPortsQuery(serverId),
		enabled: serverId !== "",
	});

	const listen = Number(listenPort);
	const listenFilled = listenPort.trim() !== "";
	const portCheck = listenFilled
		? checkPort(
				protocol,
				listen,
				validateListenPort(listen),
				ports.data?.items ?? [],
			)
		: null;

	const filledCidrs = cleanList(cidrs);
	const filledDeny = cleanList(denyCidrs);
	const accessCheck = checkRules(cidrs, denyCidrs, {
		allowRequired: !openToInternet,
	});
	const targetPortNumber = Number(targetPort);
	const sourceError =
		source === "service"
			? serviceId === ""
				? "Pick the service to expose."
				: !(targetPortNumber >= 1 && targetPortNumber <= 65535)
					? "Enter the port the service listens on (1 to 65535)."
					: null
			: address.trim() === ""
				? "Enter host:port."
				: validateUpstreamAddress(address);
	const accessError =
		protocol === "tcp" && filledCidrs.length === 0 && !openToInternet
			? "Add an allowed network, or open the port to the internet on purpose."
			: null;
	const valid =
		serverId !== "" &&
		sourceError === null &&
		portCheck?.state === "ok" &&
		(protocol !== "tcp" || (accessError === null && rulesOk(accessCheck)));

	const suggestedName =
		name.trim() !== ""
			? name.trim()
			: `${chosen?.name ?? "port"}-${protocol}-${listenPort || "port"}`;

	const submit = (event: FormEvent) => {
		event.preventDefault();
		setTouched(true);
		if (!valid || create.isPending) return;
		const body: CreateRouteInputBody = {
			name: suggestedName,
			serverId,
			protocol,
			listenPort: listen,
			upstreams:
				source === "service"
					? [{ serviceId, port: targetPortNumber }]
					: [{ address: address.trim() }],
			...(protocol === "tcp"
				? {
						...(filledCidrs.length > 0
							? { allowCidrs: filledCidrs }
							: { openToInternet: true }),
						...(filledDeny.length > 0 ? { denyCidrs: filledDeny } : {}),
					}
				: {}),
		};
		create.mutate(body);
	};

	const lines = l4PlanLines({
		protocol,
		listenPort: Number.isFinite(listen) ? listen : 0,
		newPort: portCheck?.state === "ok",
		serverName: server?.name ?? "this server",
		allowCidrs: filledCidrs,
		denyCidrs: filledDeny,
		openToInternet,
	});
	const listenProblem =
		portCheck && portCheck.state !== "ok"
			? portCheck.message
			: fieldError(create.error, "listenPort");
	const upstreamProblem = fieldError(create.error, "upstream");

	return (
		<form onSubmit={submit} className="contents">
			<DialogHeader>
				<DialogTitle>TCP/UDP port</DialogTitle>
				<DialogDescription>
					Open a port on a server and forward it to a service: a game server,
					SMTP, a database for a trusted network.
				</DialogDescription>
			</DialogHeader>
			<DialogBody>
				{isAgentTooOld(create.error) ? <AgentTooOldBanner /> : null}
				<Field>
					<FieldLabel>Protocol</FieldLabel>
					<div>
						<Segmented
							aria-label="Protocol"
							font="sans"
							density="regular"
							value={protocol}
							onValueChange={setProtocol}
							options={[
								{ value: "tcp", label: "TCP" },
								{ value: "udp", label: "UDP" },
							]}
						/>
					</div>
				</Field>
				<Field>
					<FieldLabel>Forward to</FieldLabel>
					<div>
						<Segmented
							aria-label="Where the port goes"
							font="sans"
							density="regular"
							value={source}
							onValueChange={setSource}
							options={[
								{ value: "service", label: "A service here" },
								{ value: "address", label: "An address" },
							]}
						/>
					</div>
				</Field>
				{source === "service" ? (
					<div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_120px]">
						<Field>
							<FieldLabel htmlFor="port-service">Service</FieldLabel>
							<Select
								value={serviceId === "" ? null : serviceId}
								items={candidates.map((s) => ({ value: s.id, label: s.name }))}
								onValueChange={(next) => {
									const id = String(next ?? "");
									setServiceId(id);
									const picked = candidates.find((s) => s.id === id);
									const port = picked ? servicePort(picked) : undefined;
									if (port) setTargetPort(String(port));
								}}
							>
								<SelectTrigger
									id="port-service"
									className="w-full"
									aria-invalid={touched && serviceId === ""}
								>
									<SelectValue placeholder="Pick a service" />
								</SelectTrigger>
								<SelectContent alignItemWithTrigger={false}>
									{candidates.map((s) => (
										<SelectItem key={s.id} value={s.id}>
											<span className="flex flex-col">
												<span>{s.name}</span>
												<span className="text-2xs text-ink-3">
													on {s.server?.name ?? "no server"}
												</span>
											</span>
										</SelectItem>
									))}
								</SelectContent>
							</Select>
							{request.target && !target && services.isSuccess ? (
								<FieldError>
									That service is not in this environment.
								</FieldError>
							) : null}
						</Field>
						<Field>
							<FieldLabel htmlFor="port-target">Its port</FieldLabel>
							<Input
								id="port-target"
								inputMode="numeric"
								className="font-mono"
								placeholder="25565"
								value={targetPort}
								onChange={(event) => setTargetPort(event.target.value)}
							/>
						</Field>
					</div>
				) : (
					<>
						<Field>
							<FieldLabel htmlFor="port-server">Published by</FieldLabel>
							<ServerSelect
								id="port-server"
								servers={usable}
								value={addressServerId}
								onChange={setAddressServerId}
								invalid={touched && addressServerId === ""}
							/>
						</Field>
						<Field>
							<FieldLabel htmlFor="port-address">Address</FieldLabel>
							<div className="flex items-center gap-1.5">
								<Input
									id="port-address"
									autoComplete="off"
									spellCheck={false}
									className="font-mono"
									placeholder="192.168.1.20:25565"
									aria-invalid={touched && sourceError !== null}
									value={address}
									onChange={(event) => setAddress(event.target.value)}
								/>
								<TailnetDevicePicker
									serverId={addressServerId}
									onPick={(host) => setAddress(withHost(address, host))}
								/>
							</div>
						</Field>
					</>
				)}
				{touched && sourceError ? <FieldError>{sourceError}</FieldError> : null}
				{upstreamProblem ? <FieldError>{upstreamProblem}</FieldError> : null}
				<Field>
					<FieldLabel htmlFor="port-listen">Port on the server</FieldLabel>
					<Input
						id="port-listen"
						inputMode="numeric"
						className="max-w-[160px] font-mono"
						placeholder="25565"
						aria-invalid={listenProblem !== undefined && listenFilled}
						value={listenPort}
						onChange={(event) => setListenPort(event.target.value)}
					/>
					{listenFilled && portCheck?.state === "ok" && !ports.isPending ? (
						<p className="text-ok text-xs">
							{protocol.toUpperCase()} {listen} is free on{" "}
							{server?.name ?? "this server"}.
						</p>
					) : null}
					{serverId !== "" && ports.isError ? (
						<p className="text-ink-3 text-xs">
							The server's ports could not be checked; the control plane still
							refuses a port that is taken.
						</p>
					) : null}
					<FieldDescription>
						1024 to 65535. Visitors connect to this port on the server's
						address.
					</FieldDescription>
					{listenFilled && listenProblem ? (
						<FieldError>{listenProblem}</FieldError>
					) : null}
				</Field>

				{protocol === "tcp" ? (
					<Field>
						<FieldLabel>Who may connect</FieldLabel>
						<AccessRulesEditor
							allow={cidrs}
							deny={denyCidrs}
							check={accessCheck}
							allowRequired={!openToInternet}
							allowEmptyHint="Add an allowed network, or open the port to the internet below."
							onAllowChange={(next) => {
								setCidrs(next);
								if (cleanList(next).length > 0) setOpenToInternet(false);
							}}
							onDenyChange={setDenyCidrs}
						/>
						<CheckLabel
							className="items-start gap-2.5 text-sm"
							checkboxClassName="mt-0.5"
							checked={openToInternet && filledCidrs.length === 0}
							disabled={filledCidrs.length > 0}
							onCheckedChange={setOpenToInternet}
						>
							<span className="flex flex-col gap-0.5">
								<span className="text-ink-1">Open to the internet</span>
								<span className="text-ink-3 text-xs">
									Anyone who can reach the server can connect, except the denied
									networks. Use it only for a service that does its own
									authentication.
								</span>
							</span>
						</CheckLabel>
						{touched && accessError ? (
							<FieldError>{accessError}</FieldError>
						) : null}
					</Field>
				) : (
					<Alert variant="warning">
						<AlertDescription>{UDP_WARNING}</AlertDescription>
					</Alert>
				)}

				<Field>
					<FieldLabel htmlFor="port-name">Name</FieldLabel>
					<Input
						id="port-name"
						autoComplete="off"
						placeholder={suggestedName}
						value={name}
						onChange={(event) => setName(event.target.value)}
					/>
				</Field>

				<Alert variant="warning">
					<AlertDescription>
						<p>{RESTART_WARNING}</p>
						<p className="mt-1">{FIREWALL_NOTE}</p>
					</AlertDescription>
				</Alert>
				{lines.length > 0 ? (
					<ul className="list-disc pl-5 text-ink-3 text-xs">
						{lines.map((line) => (
							<li key={line}>{line}</li>
						))}
					</ul>
				) : null}
				{create.isError &&
				!isAgentTooOld(create.error) &&
				!upstreamProblem &&
				!fieldError(create.error, "listenPort") ? (
					<FieldError>{errorMessage(create.error)}</FieldError>
				) : null}
			</DialogBody>
			<DialogFooter>
				<Button
					type="button"
					variant="ghost"
					onClick={() => routeCreate.close()}
				>
					Cancel
				</Button>
				<Button type="submit" disabled={create.isPending}>
					{create.isPending ? <Spinner /> : null}
					Create port
				</Button>
			</DialogFooter>
		</form>
	);
}
