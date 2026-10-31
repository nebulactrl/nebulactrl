import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { errorCode, errorMessage } from "#/api/client-config";
import { createPlatformService, retryPlatformService } from "#/api/gen/sdk.gen";
import type {
	PlatformProvisionBody,
	PlatformServiceView,
} from "#/api/gen/types.gen";
import { serversQuery } from "#/api/hooks";
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
import {
	Field,
	FieldDescription,
	FieldError,
	FieldLabel,
} from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { ServerPicker } from "#/features/create/create-dialog";
import { derpDisclosure } from "#/features/mesh/model";
import {
	type CreateFailure,
	createFailure,
	EMPTY_DRAFT,
	firstProblem,
	HEADSCALE,
	hostnameRequired,
	isBusy,
	kindEntry,
	provisionBody,
	type SetupDraft,
	setupProblems,
	stepRows,
	wiringTarget,
} from "./model";
import { StatusLine, StepList, WarningNote } from "./parts";
import {
	invalidatePlatform,
	platformServiceQuery,
	platformServicesQuery,
	usableServers,
} from "./queries";

export interface PlatformSetupDialogProps {
	open: boolean;
	onOpenChange(open: boolean): void;
	/** Prefilled options, e.g. a hostname. */
	defaults?: Partial<SetupDraft> | undefined;
}

/**
 * "Set up Headscale for me": a compact dialog that asks only what cannot be chosen for
 * you (a server, a name), says what it will run and what it costs, then
 * follows the service until it is ready or failed.
 */
export function PlatformSetupDialog({
	open,
	onOpenChange,
	defaults,
}: PlatformSetupDialogProps) {
	// One body per opening: the form and the progress start fresh each time.
	const [session, setSession] = useState(0);
	const wasOpen = useRef(false);
	useEffect(() => {
		if (open && !wasOpen.current) setSession((n) => n + 1);
		wasOpen.current = open;
	}, [open]);

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="lg">
				<SetupBody
					key={session}
					defaults={defaults}
					onClose={() => onOpenChange(false)}
				/>
			</DialogContent>
		</Dialog>
	);
}

function SetupBody({
	defaults,
	onClose,
}: {
	defaults: Partial<SetupDraft> | undefined;
	onClose(): void;
}) {
	const queryClient = useQueryClient();
	const listing = useQuery(platformServicesQuery());
	const servers = useQuery(serversQuery());
	const catalog = kindEntry(listing.data, HEADSCALE);
	const [draft, setDraft] = useState<SetupDraft>({
		...EMPTY_DRAFT,
		...defaults,
	});
	const [failure, setFailure] = useState<CreateFailure | null>(null);
	const [hostnameForced, setHostnameForced] = useState(false);
	const [created, setCreated] = useState<PlatformProvisionBody | null>(null);
	const set = <K extends keyof SetupDraft>(key: K, value: SetupDraft[K]) =>
		setDraft((prev) => ({ ...prev, [key]: value }));

	const usable = usableServers(servers.data ?? []);
	// It runs on one of the team's servers, defaulting to the first online.
	const serverId = usable.some((server) => server.id === draft.serverId)
		? draft.serverId
		: (usable[0]?.id ?? "");
	const server = usable.find((entry) => entry.id === serverId);

	const hostnameNeeded = hostnameForced || hostnameRequired(server);
	const problems = setupProblems({ ...draft, serverId }, { hostnameNeeded });
	const problem = firstProblem(problems);
	// An empty required hostname is guidance and a disabled button, not an error, until something is typed.
	const hostnameShown =
		draft.hostname.trim() === "" ? undefined : problems.hostname;

	const create = useMutation({
		mutationFn: async (replace: boolean) => {
			const { data } = await createPlatformService({
				body: provisionBody({ ...draft, serverId }, { replace }),
				throwOnError: true,
			});
			return data;
		},
		onSuccess: async (data) => {
			setFailure(null);
			setCreated(data);
			await invalidatePlatform(queryClient);
		},
		onError: (error) => {
			const next = createFailure(errorCode(error), errorMessage(error));
			if (next.action === "hostname") setHostnameForced(true);
			setFailure(next);
		},
	});

	if (created) {
		return <SetupProgress created={created} onClose={onClose} />;
	}

	if (!catalog) {
		return (
			<>
				<DialogHeader>
					<DialogTitle>Set up Headscale for me</DialogTitle>
				</DialogHeader>
				<DialogBody>
					{listing.isError ? (
						<Alert variant="destructive">
							<AlertTitle>Headscale did not load</AlertTitle>
							<AlertDescription>{errorMessage(listing.error)}</AlertDescription>
						</Alert>
					) : (
						<Skeleton className="h-40 rounded-card" />
					)}
				</DialogBody>
				<DialogFooter>
					<Button type="button" variant="ghost" onClick={onClose}>
						Close
					</Button>
				</DialogFooter>
			</>
		);
	}

	const submit = (replace: boolean) => {
		if (problem || create.isPending) return;
		setFailure(null);
		create.mutate(replace);
	};

	return (
		<form
			className="contents"
			onSubmit={(event) => {
				event.preventDefault();
				submit(false);
			}}
		>
			<DialogHeader>
				<DialogTitle>Set up {catalog.label} for me</DialogTitle>
				<DialogDescription>{catalog.summary}</DialogDescription>
			</DialogHeader>
			<DialogBody>
				<p className="m-0 text-ink-3 text-xs leading-relaxed">
					Runs {catalog.product} as a service in the NebulaCtrl platform
					project. {catalog.footprint}. Reachable from your servers and tailnet
					only.
				</p>
				<p
					data-testid="derp-disclosure"
					className="surface-well m-0 rounded-card p-3 text-ink-2 text-xs leading-relaxed"
				>
					{derpDisclosure("headscale")}
				</p>

				<Field>
					<FieldLabel id="platform-server-label">Server</FieldLabel>
					{servers.isPending ? (
						<Skeleton className="h-16 rounded-ctl" />
					) : usable.length === 0 ? (
						<Alert variant="warning">
							<AlertTitle>No server is online</AlertTitle>
							<AlertDescription>
								{catalog.label} runs on one of your servers.{" "}
								<Link
									to="/servers"
									search={{ enroll: true }}
									className="text-accent-ink underline"
									onClick={onClose}
								>
									Add a server
								</Link>{" "}
								or start one, then come back.
							</AlertDescription>
						</Alert>
					) : (
						<ServerPicker
							servers={usable}
							value={serverId}
							onChange={(id) => set("serverId", id)}
						/>
					)}
					<FieldError>{problems.server}</FieldError>
				</Field>

				<Field>
					<FieldLabel htmlFor="platform-hostname">
						Hostname{hostnameNeeded ? "" : " (optional)"}
					</FieldLabel>
					<Input
						id="platform-hostname"
						autoComplete="off"
						spellCheck={false}
						className="font-mono"
						placeholder={
							hostnameNeeded
								? "headscale.example.com"
								: "Generated for you under the server's base domain"
						}
						aria-invalid={hostnameShown !== undefined}
						value={draft.hostname}
						onChange={(event) => set("hostname", event.target.value)}
					/>
					{hostnameShown === undefined ? (
						<FieldDescription>
							{hostnameNeeded
								? "This server has no domain to make a name from. Point a DNS record at it and enter the name."
								: "Leave empty to use a generated name. Point your own DNS name here if you prefer."}
						</FieldDescription>
					) : null}
					<FieldError>{hostnameShown}</FieldError>
				</Field>

				<details>
					<summary className="cursor-pointer select-none rounded-sm text-ink-2 text-sm outline-none hover:text-ink-1 focus-visible:focus-ring">
						Advanced
					</summary>
					<div className="pt-3">
						<Field>
							<FieldLabel htmlFor="platform-public-url">
								Address override
							</FieldLabel>
							<Input
								id="platform-public-url"
								autoComplete="off"
								spellCheck={false}
								className="font-mono"
								placeholder="https://<hostname>"
								aria-invalid={problems.publicUrl !== undefined}
								value={draft.publicUrl}
								onChange={(event) => set("publicUrl", event.target.value)}
							/>
							<FieldDescription>
								Only for a proxy on a non-standard port or a plain-http test. It
								must use the hostname above and stays internal.
							</FieldDescription>
							<FieldError>{problems.publicUrl}</FieldError>
						</Field>
					</div>
				</details>

				{catalog.notice ? (
					<Alert variant="info">
						<AlertDescription>{catalog.notice}</AlertDescription>
					</Alert>
				) : null}

				{failure?.action === "replace" ? (
					<Alert variant="warning">
						<AlertTitle>A Tailscale integration already exists</AlertTitle>
						<AlertDescription className="flex flex-col gap-2">
							<span>{failure.message}</span>
							<span className="flex flex-wrap gap-2">
								<Button
									type="button"
									size="sm"
									variant="danger"
									disabled={problem !== null || create.isPending}
									onClick={() => submit(true)}
								>
									Replace it
								</Button>
								<Button
									type="button"
									size="sm"
									variant="ghost"
									onClick={() => setFailure(null)}
								>
									Keep it
								</Button>
							</span>
						</AlertDescription>
					</Alert>
				) : failure ? (
					<Alert variant="destructive">
						<AlertDescription>{failure.message}</AlertDescription>
					</Alert>
				) : null}
			</DialogBody>
			<DialogFooter>
				<Button type="button" variant="ghost" onClick={onClose}>
					Cancel
				</Button>
				<Button
					type="submit"
					variant={problem ? "secondary" : "primary"}
					disabled={problem !== null || create.isPending}
					title={problem ?? undefined}
				>
					{create.isPending ? <Spinner /> : null}
					Set up
				</Button>
			</DialogFooter>
		</form>
	);
}

/** The followed service: stages while it works, then what it was wired into. */
function ServiceProgress({ initial }: { initial: PlatformServiceView }) {
	const queryClient = useQueryClient();
	const followed = useQuery({
		...platformServiceQuery(initial.id),
		initialData: initial,
	});
	const view = followed.data;
	const retry = useMutation({
		mutationFn: async () => {
			await retryPlatformService({ path: { id: view.id }, throwOnError: true });
		},
		onSuccess: () => invalidatePlatform(queryClient),
		onError: (error) => toast.error("Could not retry", errorMessage(error)),
	});

	return (
		<section
			aria-label={view.label}
			className="surface-well flex flex-col gap-3 rounded-card p-3.5"
		>
			<div className="flex flex-wrap items-center justify-between gap-2">
				<h3 className="m-0 font-medium text-ink-1 text-sm">{view.label}</h3>
				<StatusLine view={view} />
			</div>
			{view.status === "ready" ? (
				<ReadySummary view={view} />
			) : view.status === "failed" ? (
				<>
					<StepList rows={stepRows(view)} />
					<Alert variant="destructive">
						<AlertTitle>Set-up failed</AlertTitle>
						<AlertDescription>
							{view.message || "It stopped without saying why."}
						</AlertDescription>
					</Alert>
					<div>
						<Button
							type="button"
							variant="secondary"
							size="sm"
							disabled={retry.isPending}
							onClick={() => retry.mutate()}
						>
							{retry.isPending ? <Spinner /> : null}
							Retry
						</Button>
					</div>
				</>
			) : (
				<>
					<StepList rows={stepRows(view)} />
					{view.message ? (
						<p className="m-0 text-ink-3 text-xs">{view.message}</p>
					) : null}
				</>
			)}
		</section>
	);
}

/** What a ready service was wired into, and its address. */
function ReadySummary({ view }: { view: PlatformServiceView }) {
	const target = view.wiring ? wiringTarget(view.wiring.type) : null;
	return (
		<div className="flex flex-col gap-3">
			{view.wiring && target ? (
				<p className="m-0 text-ink-2 text-xs leading-relaxed">
					Connected {target.noun}, <strong>{view.wiring.name}</strong>. Find it
					in{" "}
					<Link
						to="/settings/$section"
						params={{ section: target.slug }}
						className="text-accent-ink underline"
					>
						Team settings → {target.section}
					</Link>
					.
				</p>
			) : (
				<p className="m-0 text-ink-2 text-xs leading-relaxed">
					It is running{view.serverName ? ` on ${view.serverName}` : ""}.
				</p>
			)}
			{view.publicUrl ? (
				<p className="m-0 truncate font-mono text-ink-3 text-xs">
					{view.publicUrl}
				</p>
			) : null}
			{(view.warnings ?? []).map((warning) => (
				<WarningNote key={warning}>{warning}</WarningNote>
			))}
		</div>
	);
}

/** After submit: follows the service with its stages in words. */
function SetupProgress({
	created,
	onClose,
}: {
	created: PlatformProvisionBody;
	onClose(): void;
}) {
	const primary = useQuery({
		...platformServiceQuery(created.service.id),
		initialData: created.service,
	}).data;
	const finished = !isBusy(primary);

	return (
		<>
			<DialogHeader>
				<DialogTitle>
					{primary.status === "ready"
						? `${primary.label} is ready`
						: primary.status === "failed"
							? `${primary.label} did not finish`
							: `Setting up ${primary.label}`}
				</DialogTitle>
				<DialogDescription>
					{finished
						? "Everything NebulaCtrl set up is listed below."
						: "You can close this: it keeps going in the background and the card in Team settings → Private network (Headscale) follows it."}
				</DialogDescription>
			</DialogHeader>
			<DialogBody>
				<ServiceProgress initial={created.service} />
			</DialogBody>
			<DialogFooter>
				<Button
					type="button"
					variant={finished ? "primary" : "secondary"}
					onClick={onClose}
				>
					{finished ? "Done" : "Close"}
				</Button>
			</DialogFooter>
		</>
	);
}
