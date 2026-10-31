import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { cn } from "cn";
import {
	type FormEvent,
	type KeyboardEvent,
	useMemo,
	useRef,
	useState,
} from "react";
import { errorMessage } from "#/api/client-config";
import { listGitConnectionsOptions } from "#/api/gen/@tanstack/react-query.gen";
import {
	createService,
	deployService,
	linkServiceGit,
	putVariables,
	scanServiceEnvTemplates,
	setServiceScaling,
} from "#/api/gen/sdk.gen";
import type {
	CreateServiceInputBody,
	EnvTemplatePreviewInput,
} from "#/api/gen/types.gen";
import { invalidateServices } from "#/api/invalidate";
import {
	type Environment,
	type Server,
	type Service,
	toService,
} from "#/api/models";
import { CredentialHelp } from "#/components/common/credential-help";
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
import {
	Field,
	FieldDescription,
	FieldError,
	FieldLabel,
} from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Progress } from "#/components/ui/progress";
import { Segmented } from "#/components/ui/segmented";
import { Spinner } from "#/components/ui/spinner";
import { StatusIcon } from "#/components/ui/status-icon";
import { Stepper } from "#/components/ui/stepper";
import { toast } from "#/components/ui/toast";
import { ServiceIcon } from "#/features/canvas/service-icon";
import { placeNode } from "#/features/canvas/use-layout-persistence";
import { openStagedReview } from "#/features/deploy-button";
import { isSchedulable, unschedulableReason } from "#/features/fleet/model";
import {
	RepoPicker,
	type RepoSelection,
	usableConnections,
} from "#/features/git/repo-picker";
import {
	type ExampleChoices,
	mergeChoices,
	toWrites,
} from "#/features/variables/env-example";
import { gitCloneGuide } from "#/lib/credential-links";
import {
	type Capacity,
	capacityOf,
	parsePort,
	REPLICAS_MAX,
	replicasEligible,
	suggestName,
} from "./lib";
import { type CreateRequest, createActions } from "./store";
import { useEnvTemplatePreview } from "./use-env-preview";
import { EnvPreviewNote, VariablesStep } from "./variables-step";

/**
 * A repository URL worth reading while it is typed: http(s), a host and an
 * owner/name path. SSH URLs wait for the service: its deploy key does not
 * exist yet.
 */
const READABLE_URL = /^https?:\/\/[^/\s]+\/[^/\s]+\/\S+$/;

const TITLE: Record<ServiceRequest["kind"], string> = {
	git: "New service from a Git repository",
	image: "New service from a Docker image",
};

const DESCRIPTION: Record<ServiceRequest["kind"], string> = {
	git: "The repository is built on the server you pick: with its Dockerfile if it has one, otherwise with Railpack.",
	image: "The image is pulled onto the server you pick.",
};

const pct = (value: number | null) =>
	value === null ? "—" : `${Math.round(value)}%`;

function Meter({ label, value }: { label: string; value: number | null }) {
	return (
		<div className="min-w-0 flex-1">
			<div className="mb-1 flex items-baseline justify-between gap-1 text-2xs">
				<span className="text-ink-3">{label}</span>
				<span className="font-mono text-ink-2 tabular-nums">{pct(value)}</span>
			</div>
			<Progress
				value={value ?? 0}
				size="hairline"
				warnAt={85}
				aria-label={`${label} in use`}
				className="gap-0"
			/>
		</div>
	);
}

function Headroom({ capacity }: { capacity: Capacity | null }) {
	if (!capacity) {
		return <p className="text-2xs text-ink-3">No heartbeat sample yet.</p>;
	}
	return (
		<div className="flex gap-3">
			<Meter label="CPU" value={capacity.cpu} />
			<Meter label="Memory" value={capacity.mem} />
			<Meter label="Disk" value={capacity.disk} />
		</div>
	);
}

/**
 * The servers a service can start on, as a radio group: each shows how much
 * of its CPU, memory and disk is in use right now. One that is draining or
 * drained is listed but cannot be chosen, and says why. `disabledReasons`
 * (server id → reason) disables further servers the caller knows cannot take
 * this install, with the reason in place of the headroom.
 */
export function ServerPicker({
	servers,
	value,
	onChange,
	disabledReasons,
}: {
	servers: readonly Server[];
	value: string;
	onChange(id: string): void;
	disabledReasons?: ReadonlyMap<string, string>;
}) {
	const refs = useRef<Array<HTMLButtonElement | null>>([]);
	const choosable = (server: Server) =>
		isSchedulable(server) && !disabledReasons?.has(server.id);
	const move = (from: number, step: 1 | -1) => {
		for (let offset = 1; offset <= servers.length; offset++) {
			const index =
				(from + step * offset + servers.length * offset) % servers.length;
			const candidate = servers[index];
			if (candidate && choosable(candidate)) {
				onChange(candidate.id);
				refs.current[index]?.focus();
				return;
			}
		}
	};
	const onKeyDown = (event: KeyboardEvent, index: number) => {
		if (event.key === "ArrowDown" || event.key === "ArrowRight") {
			event.preventDefault();
			move(index, 1);
		} else if (event.key === "ArrowUp" || event.key === "ArrowLeft") {
			event.preventDefault();
			move(index, -1);
		}
	};
	const selectedIndex = servers.findIndex((server) => server.id === value);

	return (
		<div
			role="radiogroup"
			aria-label="Server"
			className="flex flex-col gap-1.5"
		>
			{servers.map((server, index) => {
				const checked = server.id === value;
				const closed = isSchedulable(server)
					? null
					: unschedulableReason(server);
				const offline = server.status !== "online";
				const barred = disabledReasons?.get(server.id);
				return (
					// biome-ignore lint/a11y/useSemanticElements: a card-style radio holding a status icon, name and capacity; the group already has a roving tabindex and arrow-key selection above, which a native radio input could not render as this card.
					<button
						key={server.id}
						ref={(node) => {
							refs.current[index] = node;
						}}
						type="button"
						role="radio"
						aria-checked={checked}
						disabled={closed !== null || barred !== undefined}
						tabIndex={checked || (selectedIndex < 0 && index === 0) ? 0 : -1}
						onClick={() => onChange(server.id)}
						onKeyDown={(event) => onKeyDown(event, index)}
						className={cn(
							"surface-well flex cursor-pointer flex-col gap-2 rounded-ctl border border-line-control px-3 py-2.5 text-left outline-none transition-[border-color,background-color,box-shadow] duration-150 ease-out focus-visible:focus-ring disabled:cursor-not-allowed disabled:opacity-55",
							checked &&
								"border-accent-ink bg-accent-wash shadow-[0_0_0_1px_var(--color-accent-ink)]",
						)}
					>
						<span className="flex items-center gap-2 text-[13px]">
							<StatusIcon
								kind={
									closed || barred ? "stopped" : offline ? "offline" : "live"
								}
								label=""
							/>
							<span className="font-medium text-ink-1">{server.name}</span>
							<span
								className={cn(
									"text-2xs",
									closed || barred || offline ? "text-warn" : "text-ink-3",
								)}
							>
								{closed
									? `${closed}: not accepting services`
									: (barred ??
										(offline
											? `${server.status}: the first deploy waits until it is back`
											: "online"))}
							</span>
						</span>
						{closed || barred ? null : (
							<Headroom capacity={capacityOf(server)} />
						)}
					</button>
				);
			})}
		</div>
	);
}

/** The create request of a Git or Docker image service; databases have their own dialog. */
export type ServiceRequest = CreateRequest & { kind: "git" | "image" };

export function isServiceRequest(
	request: CreateRequest,
): request is ServiceRequest {
	return request.kind !== "database";
}

export interface CreateServiceDialogProps {
	request: ServiceRequest | null;
	environment: Pick<Environment, "id" | "name" | "isProduction">;
	servers: readonly Server[];
}

/**
 * The compact create dialog: what it runs, what it is called, where it
 * runs. Creating does not deploy; the toast that follows offers it.
 */
export function CreateServiceDialog({
	request,
	environment,
	servers,
}: CreateServiceDialogProps) {
	// Keep the last request while the dialog animates closed.
	const last = useRef<ServiceRequest>({ kind: "image" });
	if (request !== null) last.current = request;
	return (
		<Dialog
			open={request !== null}
			onOpenChange={(next) => !next && createActions.closeDialog()}
		>
			<DialogContent width={520}>
				<CreateForm
					request={last.current}
					environment={environment}
					servers={servers}
				/>
			</DialogContent>
		</Dialog>
	);
}

/** Mounts with the dialog, so every opening starts from a clean form. */
function CreateForm({
	request,
	environment,
	servers,
}: {
	request: ServiceRequest;
	environment: Pick<Environment, "id" | "name" | "isProduction">;
	servers: readonly Server[];
}) {
	const { kind } = request;
	const queryClient = useQueryClient();
	const usable = useMemo(
		() =>
			servers.filter(
				(server) => server.status !== "revoked" && server.status !== "pending",
			),
		[servers],
	);
	const sourceKind: "image" | "git" = kind === "git" ? "git" : "image";

	const [name, setName] = useState("");
	const [nameTouched, setNameTouched] = useState(false);
	const [image, setImage] = useState("");
	const [gitUrl, setGitUrl] = useState("");
	const [gitBranch, setGitBranch] = useState("main");
	const [gitToken, setGitToken] = useState("");
	const gitGuide = gitCloneGuide(gitUrl);
	// Pick from a connected git account, or paste any URL.
	const gitConnections = useQuery({
		...listGitConnectionsOptions(),
		enabled: kind === "git",
	});
	const hasAccounts =
		usableConnections(gitConnections.data?.items ?? []).length > 0;
	const [gitMode, setGitMode] = useState<"account" | "url">("account");
	const [gitSel, setGitSel] = useState<RepoSelection | null>(null);
	const accountMode =
		sourceKind === "git" && hasAccounts && gitMode === "account";
	const [port, setPort] = useState("");
	const [replicas, setReplicas] = useState(1);
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
	const [step, setStep] = useState<"source" | "variables">("source");
	const [choices, setChoices] = useState<ExampleChoices>({});

	const suggested = suggestName({
		kind: sourceKind,
		image,
		gitUrl: accountMode && gitSel ? gitSel.repo : gitUrl,
	});
	const shownName = nameTouched ? name : suggested;
	const effectiveName = shownName.trim();

	const portNumber = parsePort(port);
	const portInvalid = portNumber !== undefined && Number.isNaN(portNumber);
	const showReplicas = replicasEligible({ port: portNumber });
	const sourceMissing =
		sourceKind === "image"
			? image.trim() === ""
			: accountMode
				? gitSel === null || gitSel.repo === "" || gitSel.branch === ""
				: gitUrl.trim() === "" || gitBranch.trim() === "";
	const canCreate =
		effectiveName !== "" && !sourceMissing && !portInvalid && serverId !== "";

	const previewInput = useMemo<EnvTemplatePreviewInput | null>(() => {
		if (sourceKind !== "git") return null;
		const server = serverId === "" ? {} : { serverId };
		if (accountMode) {
			if (gitSel === null || gitSel.repo === "" || gitSel.branch === "")
				return null;
			return {
				connectionId: gitSel.connectionId,
				repo: gitSel.repo,
				branch: gitSel.branch,
				...server,
			};
		}
		const url = gitUrl.trim();
		if (!READABLE_URL.test(url) || gitBranch.trim() === "") return null;
		return {
			gitUrl: url,
			branch: gitBranch.trim(),
			...(gitToken === "" ? {} : { gitToken }),
			...server,
		};
	}, [sourceKind, accountMode, gitSel, gitUrl, gitBranch, gitToken, serverId]);
	const preview = useEnvTemplatePreview(environment.id, previewInput);
	const hasKeys = preview.scan !== undefined && preview.items.length > 0;
	const reviewing = step === "variables" && hasKeys;
	// The keys the user accepted, staged together with the service.
	const writes = reviewing ? toWrites(preview.items, choices) : [];

	const deployNow = async (service: Service) => {
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
			toast.error(`Could not deploy ${service.name}`, errorMessage(failure));
		}
	};

	const create = useMutation({
		mutationFn: async (): Promise<Service> => {
			const body: CreateServiceInputBody = { name: effectiveName, serverId };
			body.kind = "app";
			body.sourceType = sourceKind;
			body.config =
				sourceKind === "image"
					? { image: image.trim() }
					: accountMode && gitSel
						? {
								gitUrl: gitSel.cloneUrl ?? `https://${gitSel.repo}.git`,
								gitBranch: gitSel.branch,
							}
						: { gitUrl: gitUrl.trim(), gitBranch: gitBranch.trim() };
			if (portNumber !== undefined) body.config.port = portNumber;
			if (sourceKind === "git" && !accountMode && gitToken !== "")
				body.gitToken = gitToken;
			const { data } = await createService({
				path: { environmentId: environment.id },
				body,
				headers: { "Idempotency-Key": idempotencyKey },
			});
			return toService(data);
		},
		onSuccess: async (service) => {
			createActions.closeDialog();
			if (accountMode && gitSel) {
				try {
					await linkServiceGit({
						path: { serviceId: service.id },
						body: {
							connectionId: gitSel.connectionId,
							repo: gitSel.repo,
							branch: gitSel.branch,
						},
					});
				} catch (failure) {
					toast.warning(
						`${service.name} is created without a repository link`,
						`Deploy on push is off: ${errorMessage(failure)}`,
					);
				}
			}
			if (showReplicas && replicas > 1) {
				try {
					await setServiceScaling({
						path: { serviceId: service.id },
						body: { replicas },
					});
				} catch (failure) {
					toast.warning(
						`${service.name} is created with 1 replica`,
						`Could not set ${replicas}: ${errorMessage(failure)}`,
					);
				}
			}
			if (writes.length > 0) {
				try {
					await putVariables({
						path: { serviceId: service.id },
						body: { variables: writes },
					});
				} catch (failure) {
					toast.warning(
						`${service.name} is created without its variables`,
						`${errorMessage(failure)} Add them from the Variables tab.`,
					);
				}
			}
			if (sourceKind === "git") {
				// Store the repository's example files as the suggestion source, so
				// the Variables tab offers the keys that were not accepted here.
				void scanServiceEnvTemplates({ path: { serviceId: service.id } })
					.then(() => invalidateServices(queryClient))
					.catch(() => undefined);
			}
			await invalidateServices(queryClient);
			if (request.position) {
				try {
					await placeNode(service.id, request.position);
				} catch {
					toast.warning(
						`${service.name} was placed automatically`,
						"Its position could not be saved where you dropped it.",
					);
				}
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
						else void deployNow(service);
					},
				},
			});
		},
	});

	const review = () => {
		setChoices((current) => mergeChoices(preview.items, current));
		setStep("variables");
	};

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (!canCreate || create.isPending) return;
		if (hasKeys && step === "source") {
			review();
			return;
		}
		create.mutate();
	};

	const iconSource =
		sourceKind === "git"
			? {
					sourceType: "git",
					config: { gitUrl: gitUrl.trim() || "https://github.com/" },
				}
			: { sourceType: "image", config: { image: image.trim() || "docker" } };

	return (
		<form onSubmit={submit} className="contents">
			<DialogHeader>
				<DialogTitle className="flex items-center gap-2.5">
					<ServiceIcon service={iconSource} size={24} />
					{reviewing ? "Variables from .env.example" : TITLE[kind]}
				</DialogTitle>
				<DialogDescription>
					{reviewing
						? "The keys your repository lists, with a value filled in where NebulaCtrl can."
						: DESCRIPTION[kind]}
				</DialogDescription>
			</DialogHeader>
			<DialogBody>
				<div className={cn("flex flex-col gap-4", reviewing && "hidden")}>
					{sourceKind === "image" ? (
						<Field>
							<FieldLabel htmlFor="create-image">Image</FieldLabel>
							<Input
								id="create-image"
								scale="lg"
								autoFocus
								autoComplete="off"
								spellCheck={false}
								className="font-mono"
								placeholder="ghcr.io/acme/app:1.4"
								value={image}
								onChange={(event) => setImage(event.target.value)}
							/>
						</Field>
					) : null}
					{sourceKind === "git" && hasAccounts ? (
						<Segmented<"account" | "url">
							aria-label="Where the repository comes from"
							font="sans"
							density="regular"
							value={gitMode}
							onValueChange={setGitMode}
							options={[
								{ value: "account", label: "Connected account" },
								{ value: "url", label: "Any URL" },
							]}
						/>
					) : null}
					{accountMode ? (
						<RepoPicker
							idPrefix="create"
							autoFocus
							connections={gitConnections.data?.items ?? []}
							value={gitSel}
							onChange={setGitSel}
						/>
					) : null}
					{sourceKind === "git" && !accountMode ? (
						<>
							<Field>
								<FieldLabel htmlFor="create-git-url">Repository URL</FieldLabel>
								<Input
									id="create-git-url"
									scale="lg"
									autoFocus
									autoComplete="off"
									spellCheck={false}
									className="font-mono"
									placeholder="https://github.com/acme/shop.git"
									value={gitUrl}
									onChange={(event) => setGitUrl(event.target.value)}
								/>
							</Field>
							<Field>
								<FieldLabel htmlFor="create-git-branch">Branch</FieldLabel>
								<Input
									id="create-git-branch"
									scale="lg"
									autoComplete="off"
									className="font-mono"
									value={gitBranch}
									onChange={(event) => setGitBranch(event.target.value)}
								/>
							</Field>
							{gitGuide ? (
								<CredentialHelp
									guide={gitGuide}
									fieldIds={["create-git-token"]}
								/>
							) : null}
							<Field>
								<FieldLabel htmlFor="create-git-token">
									Access token (private)
								</FieldLabel>
								<Input
									id="create-git-token"
									scale="lg"
									type="password"
									autoComplete="new-password"
									placeholder="optional"
									value={gitToken}
									onChange={(event) => setGitToken(event.target.value)}
								/>
							</Field>
						</>
					) : null}
					{sourceKind === "git" ? <EnvPreviewNote preview={preview} /> : null}
					<Field>
						<FieldLabel htmlFor="create-name">Name</FieldLabel>
						<Input
							id="create-name"
							scale="lg"
							autoComplete="off"
							placeholder="app"
							value={shownName}
							onChange={(event) => {
								setNameTouched(true);
								setName(event.target.value);
							}}
						/>
					</Field>
					<div className="flex items-start gap-4">
						<Field className="min-w-0 flex-1">
							<FieldLabel htmlFor="create-port">Port</FieldLabel>
							<Input
								id="create-port"
								scale="lg"
								inputMode="numeric"
								autoComplete="off"
								className="font-mono"
								placeholder="8080"
								value={port}
								aria-invalid={portInvalid}
								onChange={(event) => setPort(event.target.value)}
							/>
							<FieldDescription>
								Leave empty for a worker with no HTTP.
							</FieldDescription>
							{portInvalid ? (
								<FieldError>Use a whole number from 1 to 65535.</FieldError>
							) : null}
						</Field>
						{showReplicas ? (
							<Field className="w-auto shrink-0">
								<FieldLabel>Replicas</FieldLabel>
								<Stepper
									aria-label="Replicas"
									value={replicas}
									onValueChange={setReplicas}
									min={1}
									max={REPLICAS_MAX}
								/>
								<FieldDescription>
									1–{REPLICAS_MAX}, same server.
								</FieldDescription>
							</Field>
						) : null}
					</div>
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
										onClick={() => createActions.closeDialog()}
									>
										Add a server
									</Link>{" "}
									first; a service runs on one Docker host.
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
				</div>
				{reviewing && preview.scan ? (
					<VariablesStep
						scan={preview.scan}
						items={preview.items}
						choices={choices}
						onChoices={setChoices}
					/>
				) : null}
				{create.isError ? (
					<FieldError>{errorMessage(create.error)}</FieldError>
				) : null}
			</DialogBody>
			<DialogFooter>
				{reviewing ? (
					<Button
						type="button"
						variant="ghost"
						onClick={() => setStep("source")}
					>
						Back
					</Button>
				) : (
					<Button
						type="button"
						variant="ghost"
						onClick={() => createActions.closeDialog()}
					>
						Cancel
					</Button>
				)}
				{hasKeys && !reviewing ? (
					<Button type="submit" disabled={!canCreate}>
						Review variables
					</Button>
				) : (
					<Button type="submit" disabled={!canCreate || create.isPending}>
						{create.isPending ? <Spinner /> : null}
						Create service
					</Button>
				)}
			</DialogFooter>
		</form>
	);
}
