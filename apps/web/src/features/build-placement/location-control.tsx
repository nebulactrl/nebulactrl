import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { serversQuery } from "#/api/hooks";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Field, FieldDescription, FieldLabel } from "#/components/ui/field";
import { Segmented } from "#/components/ui/segmented";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import {
	registryOptionText,
	selectableRegistries,
} from "#/features/registries/model";
import { useRegistries } from "#/features/registries/queries";
import {
	type BuildLocation,
	type EffectiveBuild,
	locationConsequence,
	locationOptions,
	pinnableServers,
	pinProblem,
} from "./model";

/** The select's value for "no choice": a real value, because an empty one reads as "nothing picked". */
const NONE = "__none__";

/** The level a control edits. A team has nothing above it; a project and a service inherit. */
export type LocationScope = "team" | "project" | "service";

export interface LocationValue {
	location: BuildLocation;
	/** "" is automatic (a sticky choice among eligible build servers). */
	buildServerId: string;
	/** "" inherits, or, for a team, means no registry. */
	registryId: string;
}

const SCOPE_NOUN: Record<LocationScope, string> = {
	team: "team",
	project: "project",
	service: "service",
};

/**
 * Where builds run for a team, a project or a service: on the app's server, or
 * on a build server that pushes to a registry. A project and a service can
 * inherit; a project and a service can also pin one build server, and every
 * level can name its registry. `effective` is what the draft resolves to
 * (see `resolveBuild`), which is what the notes below the fields talk about.
 * The control plane stays the authority: it falls back to the app's server
 * when no build server or registry is usable, and the plan says so.
 */
export function LocationControl({
	scope,
	idPrefix,
	value,
	onChange,
	effective,
	registryParentLabel,
}: {
	scope: LocationScope;
	idPrefix: string;
	value: LocationValue;
	onChange: (next: LocationValue) => void;
	/** What the draft resolves to once inherited levels are read. */
	effective: EffectiveBuild;
	/** For a registry that inherits: "Inherit (team default: GHCR)". */
	registryParentLabel: string | null;
}) {
	const servers = useQuery(serversQuery());
	const registries = useRegistries();

	// Listing registries needs the admin scope; for anyone else the select is left out.
	const canListRegistries =
		registries.fetchStatus !== "idle" || registries.isSuccess;
	const buildServers = pinnableServers(servers.data ?? []);
	const registryList = selectableRegistries(registries.data ?? []);
	const usesBuildServer = effective.location === "build_server";
	const inherits = value.location === "inherit";
	const registryChosen = effective.registryId !== null;
	const pinnedMissing =
		value.buildServerId !== "" &&
		servers.data !== undefined &&
		!buildServers.some((server) => server.id === value.buildServerId);

	const set = (patch: Partial<LocationValue>) =>
		onChange({ ...value, ...patch });
	const inheritNoun = scope === "service" ? "project or team" : "team";

	return (
		<div className="flex min-w-0 flex-col gap-5">
			<div className="flex flex-col gap-2">
				<span
					id={`${idPrefix}-location-label`}
					className="font-medium text-ink-1 text-sm leading-[1.25]"
				>
					Build location
				</span>
				<Segmented
					value={value.location}
					onValueChange={(location) => set({ location })}
					options={locationOptions(scope)}
					aria-label={`Build location of the ${SCOPE_NOUN[scope]}`}
					font="sans"
					density="regular"
					className="self-start"
				/>
				<p className="type-secondary m-0" role="status">
					{locationConsequence(value.location, effective)}
				</p>
			</div>

			{scope !== "team" && usesBuildServer ? (
				<Field>
					<FieldLabel htmlFor={`${idPrefix}-pin`}>Build server</FieldLabel>
					<Select
						value={value.buildServerId === "" ? NONE : value.buildServerId}
						items={[
							{ value: NONE, label: "Automatic" },
							...buildServers.map((server) => ({
								value: server.id,
								label: server.name,
							})),
						]}
						onValueChange={(next) => {
							const picked = String(next ?? NONE);
							set({ buildServerId: picked === NONE ? "" : picked });
						}}
					>
						<SelectTrigger
							id={`${idPrefix}-pin`}
							className="w-full max-w-[360px]"
						>
							<SelectValue />
						</SelectTrigger>
						<SelectContent alignItemWithTrigger={false}>
							<SelectItem value={NONE}>Automatic (sticky)</SelectItem>
							{buildServers.map((server) => {
								const problem = pinProblem(server);
								return (
									<SelectItem key={server.id} value={server.id}>
										<span className="flex items-center gap-2">
											{server.name}
											{problem ? (
												<span className="text-2xs text-warning-text">
													{problem}
												</span>
											) : null}
										</span>
									</SelectItem>
								);
							})}
						</SelectContent>
					</Select>
					<FieldDescription>
						{value.buildServerId === ""
							? "Automatic picks one eligible build server for each service and keeps using it, so its build cache stays warm."
							: "Always builds here. If it is offline or cannot build, the build falls back to the app's server."}
					</FieldDescription>
					{pinnedMissing ? (
						<Alert variant="warning">
							<AlertDescription>
								The pinned server is no longer a build server. Pick another, or
								choose Automatic.
							</AlertDescription>
						</Alert>
					) : null}
				</Field>
			) : null}

			{usesBuildServer && buildServers.length === 0 && servers.isSuccess ? (
				<Alert variant="info">
					<AlertTitle>No build server yet</AlertTitle>
					<AlertDescription>
						Builds fall back to each app's server until a server has the Builds
						or Both role.{" "}
						<Link to="/servers" className="text-accent-ink underline">
							Open servers
						</Link>
					</AlertDescription>
				</Alert>
			) : null}

			{canListRegistries &&
			(scope === "team" || usesBuildServer || value.registryId !== "") ? (
				<Field>
					<FieldLabel htmlFor={`${idPrefix}-registry`}>Registry</FieldLabel>
					<Select
						value={value.registryId === "" ? NONE : value.registryId}
						items={[
							{
								value: NONE,
								label: noneLabel(scope, registryParentLabel),
							},
							...registryList.map((registry) => ({
								value: registry.id,
								label: registryOptionText(registry),
							})),
						]}
						onValueChange={(next) => {
							const picked = String(next ?? NONE);
							set({ registryId: picked === NONE ? "" : picked });
						}}
					>
						<SelectTrigger
							id={`${idPrefix}-registry`}
							className="w-full max-w-[360px]"
						>
							<SelectValue />
						</SelectTrigger>
						<SelectContent alignItemWithTrigger={false}>
							<SelectItem value={NONE}>
								{noneLabel(scope, registryParentLabel)}
							</SelectItem>
							{registryList.map((registry) => (
								<SelectItem key={registry.id} value={registry.id}>
									{registryOptionText(registry)}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
					<FieldDescription>
						Where a build server pushes the image and the app's server pulls it
						from.{" "}
						<Link
							to="/settings/$section"
							params={{ section: "image-registries" }}
							className="text-accent-ink underline"
						>
							Manage image registries
						</Link>
					</FieldDescription>
				</Field>
			) : null}

			{usesBuildServer && !registryChosen && registries.isSuccess ? (
				<Alert variant="warning">
					<AlertTitle>
						No registry, so builds stay on the app's server
					</AlertTitle>
					<AlertDescription>
						A build server needs a registry to hand the image over.{" "}
						{inherits
							? `Pick one here, or set one on the ${inheritNoun}.`
							: "Pick one above, or add a registry first."}
					</AlertDescription>
				</Alert>
			) : null}
		</div>
	);
}

function noneLabel(scope: LocationScope, parentLabel: string | null): string {
	if (scope === "team") return "No registry";
	return parentLabel === null ? "Inherit" : `Inherit (${parentLabel})`;
}
