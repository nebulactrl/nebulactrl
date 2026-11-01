import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useMemo, useRef, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { createDatabase, deployService } from "#/api/gen/sdk.gen";
import type { CreateDatabaseInputBody, EngineView } from "#/api/gen/types.gen";
import { servicesQuery } from "#/api/hooks";
import { invalidateServices } from "#/api/invalidate";
import {
	type Environment,
	type Server,
	type Service,
	toService,
} from "#/api/models";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { CheckLabel } from "#/components/ui/check-label";
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
import { OptionCard, OptionCardGroup } from "#/components/ui/option-card";
import { Segmented } from "#/components/ui/segmented";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { ServiceIcon } from "#/features/canvas/service-icon";
import { placeNode } from "#/features/canvas/use-layout-persistence";
import { ServerPicker } from "#/features/create/create-dialog";
import { type CreateRequest, createActions } from "#/features/create/store";
import { openStagedReview } from "#/features/deploy-button";
import { isSchedulable } from "#/features/fleet/model";
import {
	CATEGORY_LABEL,
	createBlocker,
	DEFAULT_DATABASE_NAME,
	defaultVersion,
	freeName,
	initialEngine,
	isValidDatabaseName,
	LICENSE_CLASS_LABEL,
	nameTaken,
	needsLicenseAck,
	sortEngines,
	takesDatabaseName,
	versionsFor,
} from "./engines";
import { enginesQuery } from "./queries";

const NO_ENGINES: EngineView[] = [];

export interface CreateDatabaseDialogProps {
	request: CreateRequest | null;
	environment: Pick<Environment, "id" | "name" | "isProduction">;
	servers: readonly Server[];
}

/**
 * The create dialog of a managed database: engine (from the catalog), version,
 * flavor, database name, server and name. A source-available licence has to
 * be ticked before Create unlocks. Creating stages the service; the toast that
 * follows offers "Deploy now" like every other create flow.
 */
export function CreateDatabaseDialog({
	request,
	environment,
	servers,
}: CreateDatabaseDialogProps) {
	// Keep the last request while the dialog animates closed.
	const last = useRef<CreateRequest>({ kind: "database" });
	if (request !== null) last.current = request;
	return (
		<Dialog
			open={request !== null}
			onOpenChange={(next) => !next && createActions.closeDialog()}
		>
			<DialogContent width={560}>
				<DatabaseForm
					request={last.current}
					environment={environment}
					servers={servers}
				/>
			</DialogContent>
		</Dialog>
	);
}

function EngineChoice({
	engines,
	value,
	onChange,
}: {
	engines: readonly EngineView[];
	value: string;
	onChange(id: string): void;
}) {
	return (
		<OptionCardGroup
			aria-label="Database engine"
			value={value}
			onValueChange={onChange}
			className="grid grid-cols-2 gap-2"
		>
			{engines.map((engine) => (
				<OptionCard
					key={engine.id}
					value={engine.id}
					className="min-w-0"
					title={
						<span className="flex min-w-0 items-center gap-2">
							<ServiceIcon
								service={{
									kind: "database",
									config: { database: { engine: engine.id } },
								}}
								size={20}
							/>
							<span className="truncate">{engine.label}</span>
							{engine.default ? <Badge variant="accent">default</Badge> : null}
							{needsLicenseAck(engine) ? (
								<Badge
									variant="warning"
									title="Source-available licence: review its terms before you create this database"
								>
									licence
								</Badge>
							) : null}
						</span>
					}
					description={CATEGORY_LABEL[engine.category]}
				/>
			))}
		</OptionCardGroup>
	);
}

function LicenseNotice({
	engine,
	accepted,
	onChange,
}: {
	engine: EngineView;
	accepted: boolean;
	onChange(next: boolean): void;
}) {
	return (
		<Alert variant="warning">
			<AlertTitle>
				{LICENSE_CLASS_LABEL[engine.licenseClass]}: {engine.license}
			</AlertTitle>
			<AlertDescription className="flex flex-col gap-2.5">
				{engine.licenseNote ? <p>{engine.licenseNote}</p> : null}
				<CheckLabel
					checked={accepted}
					onCheckedChange={onChange}
					className="items-start"
					checkboxClassName="mt-0.5"
					labelClassName="text-ink-1"
				>
					I have reviewed the licence
				</CheckLabel>
			</AlertDescription>
		</Alert>
	);
}

/** Mounts with the dialog, so every opening starts from a clean form. */
function DatabaseForm({
	request,
	environment,
	servers,
}: {
	request: CreateRequest;
	environment: Pick<Environment, "id" | "name" | "isProduction">;
	servers: readonly Server[];
}) {
	const queryClient = useQueryClient();
	const catalog = useQuery(enginesQuery());
	const engines = useMemo(
		() => sortEngines(catalog.data?.engines ?? NO_ENGINES),
		[catalog.data],
	);
	const usable = useMemo(
		() =>
			servers.filter(
				(server) => server.status !== "revoked" && server.status !== "pending",
			),
		[servers],
	);

	const services = useQuery(servicesQuery(environment.id));
	const taken = useMemo(
		() => (services.data ?? []).map((service) => service.name),
		[services.data],
	);

	const [engineId, setEngineId] = useState<string | null>(null);
	const [flavor, setFlavor] = useState("");
	const [versionId, setVersionId] = useState<string | null>(null);
	const [databaseName, setDatabaseName] = useState("");
	const [name, setName] = useState("");
	const [nameTouched, setNameTouched] = useState(false);
	const [licenseAccepted, setLicenseAccepted] = useState(false);
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

	const engine =
		engines.find((entry) => entry.id === engineId) ??
		initialEngine(engines, request.engine);
	const flavorId = engine && flavor !== "" ? flavor : undefined;
	const versions = engine ? versionsFor(engine, flavorId) : [];
	const version =
		versions.find((entry) => entry.id === versionId)?.id ??
		(engine ? defaultVersion(engine, flavorId) : undefined);
	const chosenVersion = versions.find((entry) => entry.id === version);
	const flavors = engine?.flavors ?? [];

	const showName = engine ? takesDatabaseName(engine) : false;
	const nameInvalid =
		showName && databaseName !== "" && !isValidDatabaseName(databaseName);
	const shownName = nameTouched ? name : freeName(engine?.id ?? "", taken);
	const effectiveName = shownName.trim();

	const blocker = createBlocker({
		engine,
		version,
		databaseName,
		serverId,
		name: effectiveName,
		licenseAccepted,
		taken,
	});

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
			if (!engine || !version) throw new Error("Choose an engine.");
			const body: CreateDatabaseInputBody = {
				engine: engine.id as CreateDatabaseInputBody["engine"],
				name: effectiveName,
				version,
				serverId,
			};
			if (flavorId) body.flavor = flavorId as "postgis";
			if (showName && databaseName !== "") body.databaseName = databaseName;
			if (needsLicenseAck(engine)) body.acceptLicense = licenseAccepted;
			const { data } = await createDatabase({
				path: { environmentId: environment.id },
				body,
				headers: { "Idempotency-Key": idempotencyKey },
			});
			return toService(data);
		},
		onSuccess: async (service) => {
			createActions.closeDialog();
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

	const submit = (event: React.FormEvent) => {
		event.preventDefault();
		if (blocker !== null || create.isPending) return;
		create.mutate();
	};

	return (
		<form onSubmit={submit} className="contents">
			<DialogHeader>
				<DialogTitle className="flex items-center gap-2.5">
					<ServiceIcon
						service={{
							kind: "database",
							config: { database: { engine: engine?.id ?? "" } },
						}}
						size={24}
					/>
					New database
				</DialogTitle>
				<DialogDescription>
					A managed database on its own volume with generated credentials. Its
					port stays internal; other services read its connection string through
					a reference variable.
				</DialogDescription>
			</DialogHeader>
			<DialogBody>
				<Field>
					<FieldLabel>Engine</FieldLabel>
					{catalog.isPending ? (
						<div className="grid grid-cols-2 gap-2" role="status">
							<Skeleton className="h-[58px]" />
							<Skeleton className="h-[58px]" />
							<Skeleton className="h-[58px]" />
							<Skeleton className="h-[58px]" />
						</div>
					) : catalog.isError ? (
						<Alert variant="destructive">
							<AlertTitle>The engine catalog did not load</AlertTitle>
							<AlertDescription>
								{errorMessage(catalog.error)}{" "}
								<button
									type="button"
									className="text-accent-ink underline"
									onClick={() => void catalog.refetch()}
								>
									Retry
								</button>
							</AlertDescription>
						</Alert>
					) : engine ? (
						<>
							<EngineChoice
								engines={engines}
								value={engine.id}
								onChange={(id) => {
									setEngineId(id);
									setFlavor("");
									setVersionId(null);
									setLicenseAccepted(false);
								}}
							/>
							<FieldDescription>{engine.description}</FieldDescription>
						</>
					) : null}
				</Field>

				{engine && needsLicenseAck(engine) ? (
					<LicenseNotice
						engine={engine}
						accepted={licenseAccepted}
						onChange={setLicenseAccepted}
					/>
				) : engine && engine.licenseClass === "copyleft" ? (
					<p className="type-secondary">
						Licence: {engine.license} ({LICENSE_CLASS_LABEL.copyleft}).
					</p>
				) : null}

				{engine ? (
					<div className="flex items-start gap-4">
						<Field className="min-w-0 flex-1">
							<FieldLabel htmlFor="create-db-version">Version</FieldLabel>
							<Select
								value={version ?? ""}
								items={versions.map((entry) => ({
									value: entry.id,
									label: entry.label,
								}))}
								onValueChange={(next) => setVersionId(String(next ?? ""))}
							>
								<SelectTrigger id="create-db-version" className="w-full">
									<SelectValue placeholder="Choose a version" />
								</SelectTrigger>
								<SelectContent alignItemWithTrigger={false}>
									{versions.map((entry) => (
										<SelectItem key={entry.id} value={entry.id}>
											{entry.label}
											{entry.default ? " · default" : ""}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
							{chosenVersion?.notes ? (
								<FieldDescription>{chosenVersion.notes}</FieldDescription>
							) : null}
						</Field>
						{flavors.length > 0 ? (
							<Field className="w-auto shrink-0">
								<FieldLabel>Image</FieldLabel>
								<Segmented<string>
									aria-label="Image flavor"
									font="sans"
									density="regular"
									value={flavor}
									onValueChange={(next) => {
										setFlavor(next);
										setVersionId(null);
									}}
									options={[
										{ value: "", label: "Standard" },
										...flavors.map((entry) => ({
											value: entry.id,
											label: entry.label,
										})),
									]}
								/>
								{flavors.find((entry) => entry.id === flavor)?.note ? (
									<FieldDescription>
										{flavors.find((entry) => entry.id === flavor)?.note}
									</FieldDescription>
								) : null}
							</Field>
						) : null}
					</div>
				) : null}

				<div className="flex items-start gap-4">
					<Field className="min-w-0 flex-1">
						<FieldLabel htmlFor="create-db-name">Name</FieldLabel>
						<Input
							id="create-db-name"
							scale="lg"
							autoFocus
							autoComplete="off"
							placeholder={engine?.id ?? "database"}
							value={shownName}
							onChange={(event) => {
								setNameTouched(true);
								setName(event.target.value);
							}}
						/>
						{nameTaken(effectiveName, taken) ? (
							<FieldError>
								A service named {effectiveName} already exists in{" "}
								{environment.name}. Pick another name.
							</FieldError>
						) : null}
					</Field>
					{showName ? (
						<Field className="min-w-0 flex-1">
							<FieldLabel htmlFor="create-db-dbname">Database name</FieldLabel>
							<Input
								id="create-db-dbname"
								scale="lg"
								autoComplete="off"
								spellCheck={false}
								className="font-mono"
								placeholder={DEFAULT_DATABASE_NAME}
								value={databaseName}
								aria-invalid={nameInvalid}
								onChange={(event) => setDatabaseName(event.target.value)}
							/>
							{nameInvalid ? (
								<FieldError>
									Lowercase letters, digits and underscores; it starts with a
									letter or underscore (up to 63 characters).
								</FieldError>
							) : null}
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
								first; a database runs on one Docker host.
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
				{create.isError ? (
					<FieldError>{errorMessage(create.error)}</FieldError>
				) : null}
			</DialogBody>
			<DialogFooter>
				{blocker !== null && engine && !catalog.isPending ? (
					<span className="mr-auto text-2xs text-ink-3" aria-live="polite">
						{blocker}
					</span>
				) : null}
				<Button
					type="button"
					variant="ghost"
					onClick={() => createActions.closeDialog()}
				>
					Cancel
				</Button>
				<Button type="submit" disabled={blocker !== null || create.isPending}>
					{create.isPending ? <Spinner /> : null}
					Create database
				</Button>
			</DialogFooter>
		</form>
	);
}
