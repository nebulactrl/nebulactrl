import { PlusIcon, XIcon } from "lucide-react";
import { type FormEvent, useState } from "react";
import { errorMessage } from "#/api/client-config";
import type { CreateRouteInputBody } from "#/api/gen/types.gen";
import type { Environment, Server } from "#/api/models";
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
import { Spinner } from "#/components/ui/spinner";
import { routeCreate } from "../create-store";
import { fieldError, isAgentTooOld } from "../lib/errors";
import { withHost } from "../lib/tailscale";
import {
	normalizeHostname,
	validateHostname,
	validateUpstreamAddress,
} from "../lib/validate";
import { AgentTooOldBanner } from "../ui/agent-too-old";
import { TailnetDevicePicker } from "../ui/tailnet-device-picker";
import { routeServers, ServerSelect } from "./server-select";
import { useCreateRoute } from "./use-create-route";

const MAX_UPSTREAMS = 20;

/**
 * "External service": publish a service that is not managed by NebulaCtrl (a
 * Home Assistant on the LAN, a NAS on the tailnet) through the server's proxy.
 */
export function ExternalForm({
	environment,
	servers,
	position,
}: {
	environment: Pick<Environment, "id" | "name" | "isProduction">;
	servers: readonly Server[];
	position: { x: number; y: number } | undefined;
}) {
	const usable = routeServers(servers);
	const [name, setName] = useState("");
	const [serverId, setServerId] = useState(
		() =>
			usable.find((s) => s.status === "online" && s.proxy.v2)?.id ??
			usable.find((s) => s.proxy.v2)?.id ??
			"",
	);
	const [addresses, setAddresses] = useState<string[]>([""]);
	const [scheme, setScheme] = useState<"http" | "https">("http");
	const [skipVerify, setSkipVerify] = useState(false);
	const [domain, setDomain] = useState("");
	const [healthPath, setHealthPath] = useState("");
	const [touched, setTouched] = useState(false);
	const create = useCreateRoute(environment, position);

	const addressErrors = addresses.map((address) =>
		address.trim() === ""
			? "Enter host:port."
			: validateUpstreamAddress(address),
	);
	const domainError = domain.trim() === "" ? null : validateHostname(domain);
	const healthError =
		healthPath.trim() !== "" && !healthPath.trim().startsWith("/")
			? "Start with /, e.g. /health."
			: null;
	const valid =
		name.trim() !== "" &&
		serverId !== "" &&
		addressErrors.every((problem) => problem === null) &&
		domainError === null &&
		healthError === null;

	const submit = (event: FormEvent) => {
		event.preventDefault();
		setTouched(true);
		if (!valid || create.isPending) return;
		const body: CreateRouteInputBody = {
			name: name.trim(),
			serverId,
			protocol: "http",
			scheme,
			upstreams: addresses.map((address) => ({ address: address.trim() })),
			...(scheme === "https" && skipVerify ? { insecureSkipVerify: true } : {}),
			...(domain.trim() !== ""
				? { domains: [{ hostname: normalizeHostname(domain) }] }
				: {}),
			...(healthPath.trim() !== "" ? { healthPath: healthPath.trim() } : {}),
		};
		create.mutate(body);
	};

	const upstreamProblem = fieldError(create.error, "upstream");

	return (
		<form onSubmit={submit} className="contents">
			<DialogHeader>
				<DialogTitle>External service</DialogTitle>
				<DialogDescription>
					Publish something NebulaCtrl does not run, such as an app on your LAN
					or a device on your tailnet, behind a hostname on a server's proxy.
				</DialogDescription>
			</DialogHeader>
			<DialogBody>
				{isAgentTooOld(create.error) ? <AgentTooOldBanner /> : null}
				<Field>
					<FieldLabel htmlFor="route-name">Name</FieldLabel>
					<Input
						id="route-name"
						scale="lg"
						autoFocus
						autoComplete="off"
						placeholder="home-assistant"
						aria-invalid={touched && name.trim() === ""}
						value={name}
						onChange={(event) => setName(event.target.value)}
					/>
				</Field>
				<Field>
					<FieldLabel htmlFor="route-server">Published by</FieldLabel>
					<ServerSelect
						id="route-server"
						servers={usable}
						value={serverId}
						onChange={setServerId}
						invalid={touched && serverId === ""}
					/>
					<FieldDescription>
						The server whose proxy answers for this hostname and reaches the
						address below.
					</FieldDescription>
				</Field>
				<Field>
					<FieldLabel htmlFor="route-upstream-0">Where it listens</FieldLabel>
					<ul aria-label="Upstream addresses" className="flex flex-col gap-1.5">
						{addresses.map((address, index) => (
							// biome-ignore lint/suspicious/noArrayIndexKey: rows are positional inputs without identity
							<li key={index} className="flex flex-col gap-1">
								<div className="flex items-center gap-1.5">
									<Input
										id={`route-upstream-${index}`}
										autoComplete="off"
										spellCheck={false}
										className="font-mono"
										placeholder="192.168.1.20:8123"
										aria-invalid={
											(touched || address !== "") &&
											addressErrors[index] !== null
										}
										value={address}
										onChange={(event) =>
											setAddresses(
												addresses.map((item, i) =>
													i === index ? event.target.value : item,
												),
											)
										}
									/>
									{index === 0 && addresses.length === 1 ? (
										<TailnetDevicePicker
											serverId={serverId}
											onPick={(host) => setAddresses([withHost(address, host)])}
										/>
									) : null}
									{addresses.length > 1 ? (
										<Button
											type="button"
											variant="ghost"
											size="icon-sm"
											aria-label={`Remove address ${index + 1}`}
											onClick={() =>
												setAddresses(addresses.filter((_, i) => i !== index))
											}
										>
											<XIcon aria-hidden />
										</Button>
									) : null}
								</div>
								{(touched || address !== "") && addressErrors[index] ? (
									<FieldError>{addressErrors[index]}</FieldError>
								) : null}
							</li>
						))}
					</ul>
					{upstreamProblem ? <FieldError>{upstreamProblem}</FieldError> : null}
					{addresses.length < MAX_UPSTREAMS ? (
						<div>
							<Button
								type="button"
								variant="secondary"
								size="xs"
								onClick={() => setAddresses([...addresses, ""])}
							>
								<PlusIcon aria-hidden />
								Add another address
							</Button>
						</div>
					) : null}
					<FieldDescription>
						More than one address load-balances between them. Loopback and
						link-local addresses are refused unless an administrator allows them
						for the server.
					</FieldDescription>
				</Field>
				<div className="flex flex-wrap items-start gap-x-6 gap-y-3">
					<Field className="w-auto">
						<FieldLabel>Protocol to the service</FieldLabel>
						<Segmented
							aria-label="Scheme"
							font="sans"
							value={scheme}
							onValueChange={setScheme}
							options={[
								{ value: "http", label: "HTTP" },
								{ value: "https", label: "HTTPS" },
							]}
						/>
					</Field>
					{scheme === "https" ? (
						<CheckLabel
							className="mt-6 text-sm"
							checked={skipVerify}
							onCheckedChange={setSkipVerify}
						>
							Skip TLS verification
						</CheckLabel>
					) : null}
				</div>
				{scheme === "https" && skipVerify ? (
					<p className="text-warning-text text-xs">
						The proxy will accept any certificate from this service, including a
						forged one. Use it for self-signed services on a network you trust.
					</p>
				) : null}
				<Field>
					<FieldLabel htmlFor="route-domain">Hostname (optional)</FieldLabel>
					<Input
						id="route-domain"
						autoComplete="off"
						spellCheck={false}
						className="font-mono"
						placeholder="home.example.com"
						aria-invalid={domainError !== null}
						value={domain}
						onChange={(event) => setDomain(event.target.value)}
					/>
					<FieldDescription>
						Add more hostnames, paths and access rules in the route's settings.
						Without one the route has no public address yet.
					</FieldDescription>
					{domainError ? <FieldError>{domainError}</FieldError> : null}
				</Field>
				<Field>
					<FieldLabel htmlFor="route-health">Health path (optional)</FieldLabel>
					<Input
						id="route-health"
						autoComplete="off"
						spellCheck={false}
						className="max-w-[220px] font-mono"
						placeholder="/health"
						aria-invalid={healthError !== null}
						value={healthPath}
						onChange={(event) => setHealthPath(event.target.value)}
					/>
					<FieldDescription>
						The proxy stops sending traffic to an address that fails it.
					</FieldDescription>
					{healthError ? <FieldError>{healthError}</FieldError> : null}
				</Field>
				{create.isError && !isAgentTooOld(create.error) && !upstreamProblem ? (
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
					Create route
				</Button>
			</DialogFooter>
		</form>
	);
}
