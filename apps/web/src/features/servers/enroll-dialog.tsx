import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { type FormEvent, useEffect, useRef, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { createEnrollmentToken } from "#/api/gen/sdk.gen";
import type { EnrollmentToken } from "#/api/gen/types.gen";
import { Fact, FactGrid } from "#/components/common/key-value";
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
import { StatusIcon } from "#/components/ui/status-icon";
import { formatBytes } from "#/lib/format";
import { cacheEnrollment } from "./enrollment";
import { InstallCommandField } from "./install-command";
import type { ServerView } from "./model";
import { serverViewQuery, serverViewsQuery } from "./queries";

/** How often the dialog asks whether the agent has connected (SSE usually beats it). */
const CONNECT_POLL_MS = 2_000;

function Facts({ server }: { server: ServerView }) {
	const facts = server.facts;
	const rows: [string, string][] = [];
	if (facts) {
		rows.push(
			["OS", `${facts.os} · ${facts.arch}`],
			["CPUs", String(facts.cpus)],
			["Memory", formatBytes(facts.memTotal)],
			["Docker", facts.dockerVersion],
			[
				"Ports 80 / 443",
				`${facts.port80Free ? "free" : "in use"} / ${facts.port443Free ? "free" : "in use"}`,
			],
		);
	}
	if (server.agentVersion) rows.unshift(["Agent", server.agentVersion]);
	return (
		<FactGrid className="surface-well gap-y-3 rounded-card px-4 py-3.5 md:grid-cols-2">
			{rows.map(([label, value]) => (
				<Fact key={label} label={label}>
					<span title={value}>{value}</span>
				</Fact>
			))}
		</FactGrid>
	);
}

function Connect({
	enrollment,
	onClose,
}: {
	enrollment: EnrollmentToken;
	onClose: () => void;
}) {
	const server = useQuery({
		...serverViewQuery(enrollment.server.id),
		refetchInterval: (query) =>
			query.state.data?.status === "online" ? false : CONNECT_POLL_MS,
	});
	const connected = server.data?.status === "online";
	return (
		<>
			<DialogBody>
				<InstallCommandField enrollment={enrollment} />
				{connected && server.data ? (
					<div className="flex flex-col gap-3" role="status">
						<div className="flex items-center gap-3">
							<StatusIcon kind="live" label="" />
							<div className="flex flex-col gap-0.5">
								<span className="font-medium text-ink-1 text-sm">
									Agent connected
								</span>
								<span className="text-ink-3 text-xs">
									{server.data.name} is online and ready for services.
								</span>
							</div>
						</div>
						<Facts server={server.data} />
					</div>
				) : (
					<div
						role="status"
						className="surface-well flex items-center gap-3 rounded-card px-3.5 py-3"
					>
						<StatusIcon kind="building" label="" />
						<span className="text-ink-2 text-sm">
							Waiting for the agent to connect…
						</span>
					</div>
				)}
			</DialogBody>
			<DialogFooter>
				<Button variant={connected ? "secondary" : "ghost"} onClick={onClose}>
					{connected ? "Done" : "Close"}
				</Button>
				{connected ? (
					<Button
						variant="primary"
						nativeButton={false}
						render={
							<Link
								to="/servers/$serverId"
								params={{ serverId: enrollment.server.id }}
							/>
						}
					>
						Open server
					</Button>
				) : null}
			</DialogFooter>
		</>
	);
}

/**
 * Add a server: name and optional address details, then the install command
 * in an engraved well with a key to copy it, then a live wait for the agent
 * (a spinning icon) that turns into a check and the host's facts when the
 * agent connects.
 */
export function EnrollDialog({
	open,
	onOpenChange,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
}) {
	const queryClient = useQueryClient();
	const [name, setName] = useState("");
	const [publicIp, setPublicIp] = useState("");
	const [baseDomain, setBaseDomain] = useState("");
	const [acmeEmail, setAcmeEmail] = useState("");
	const [enrollment, setEnrollment] = useState<EnrollmentToken | null>(null);
	const idempotencyKey = useRef(crypto.randomUUID());

	useEffect(() => {
		if (!open) return;
		idempotencyKey.current = crypto.randomUUID();
		setName("");
		setPublicIp("");
		setBaseDomain("");
		setAcmeEmail("");
		setEnrollment(null);
	}, [open]);

	const create = useMutation({
		mutationFn: async () => {
			const { data } = await createEnrollmentToken({
				body: {
					name: name.trim(),
					...(publicIp.trim() ? { publicIp: publicIp.trim() } : {}),
					...(baseDomain.trim() ? { baseDomain: baseDomain.trim() } : {}),
					...(acmeEmail.trim() ? { acmeEmail: acmeEmail.trim() } : {}),
				},
				headers: { "Idempotency-Key": idempotencyKey.current },
			});
			return data;
		},
		onSuccess: async (data) => {
			setEnrollment(data);
			cacheEnrollment(queryClient, data);
			await queryClient.invalidateQueries({
				queryKey: serverViewsQuery().queryKey,
			});
		},
	});

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (name.trim() === "" || create.isPending) return;
		create.mutate();
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>Add a server</DialogTitle>
					<DialogDescription>
						{enrollment
							? "Run the command on the host; this dialog updates when the agent connects. Closing it is fine: the server's page keeps the command and can issue a new one."
							: "A server is a Linux host with Docker. The agent dials out to this control plane, so the host needs no open inbound port for it."}
					</DialogDescription>
				</DialogHeader>
				{enrollment ? (
					<Connect
						enrollment={enrollment}
						onClose={() => onOpenChange(false)}
					/>
				) : (
					<form onSubmit={submit} className="contents">
						<DialogBody>
							<Field>
								<FieldLabel htmlFor="server-name">Name</FieldLabel>
								<Input
									id="server-name"
									scale="lg"
									autoFocus
									autoComplete="off"
									placeholder="hetzner-01"
									value={name}
									onChange={(event) => setName(event.target.value)}
								/>
								{name.trim() === "" ? (
									<FieldDescription>
										Give the server a name to create its install command.
									</FieldDescription>
								) : null}
							</Field>
							<Field>
								<FieldLabel htmlFor="server-ip">
									Public IP (optional)
								</FieldLabel>
								<Input
									id="server-ip"
									scale="lg"
									autoComplete="off"
									inputMode="decimal"
									placeholder="203.0.113.10"
									className="font-mono"
									value={publicIp}
									onChange={(event) => setPublicIp(event.target.value)}
								/>
								<FieldDescription>
									Used to check DNS records and to generate sslip.io domains.
								</FieldDescription>
							</Field>
							<Field>
								<FieldLabel htmlFor="server-base-domain">
									Base domain (optional)
								</FieldLabel>
								<Input
									id="server-base-domain"
									scale="lg"
									autoComplete="off"
									placeholder="apps.example.com"
									className="font-mono"
									value={baseDomain}
									onChange={(event) => setBaseDomain(event.target.value)}
								/>
								<FieldDescription>
									Generated domains become service-env-project.base-domain.
								</FieldDescription>
							</Field>
							<Field>
								<FieldLabel htmlFor="server-acme">
									ACME email (optional)
								</FieldLabel>
								<Input
									id="server-acme"
									type="email"
									scale="lg"
									autoComplete="off"
									placeholder="ops@example.com"
									value={acmeEmail}
									onChange={(event) => setAcmeEmail(event.target.value)}
								/>
								<FieldDescription>
									Let's Encrypt contact for the certificates the server's proxy
									requests.
								</FieldDescription>
							</Field>
							{create.isError ? (
								<FieldError>{errorMessage(create.error)}</FieldError>
							) : null}
						</DialogBody>
						<DialogFooter>
							<Button
								type="button"
								variant="ghost"
								onClick={() => onOpenChange(false)}
							>
								Cancel
							</Button>
							<Button
								type="submit"
								variant="primary"
								disabled={name.trim() === "" || create.isPending}
							>
								{create.isPending ? <Spinner /> : null}
								Create install command
							</Button>
						</DialogFooter>
					</form>
				)}
			</DialogContent>
		</Dialog>
	);
}
