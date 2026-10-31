import { useQuery } from "@tanstack/react-query";
import { projectsQuery } from "#/api/hooks";
import type { Service } from "#/api/models";
import { DocsLink } from "#/components/docs/docs-link";
import { MoreOptions } from "#/components/settings/more-options";
import { teamBuildSettingsQuery } from "#/features/build-placement/api";
import { LocationControl } from "#/features/build-placement/location-control";
import {
	type BuildLocation,
	resolveBuild,
	toBuildFields,
} from "#/features/build-placement/model";
import { PlacementReadout } from "#/features/build-placement/placement-readout";
import { useRegistries } from "#/features/registries/queries";
import type { SettingsDraft } from "./draft";
import {
	type DraftProps,
	Section,
	SectionBody,
	SelectRow,
	TextRow,
} from "./fields";
import { usePlan } from "./use-plan";

const FROM = { service: "service", project: "project", team: "team" } as const;

const SUBMODULE_MODES = [
	{
		value: "auto",
		text: "Automatic",
		label: "Automatic (when the repository has a .gitmodules file)",
		blurb: "Checked out when the commit has a .gitmodules file.",
		info: "Submodules are checked out recursively. A submodule that cannot be fetched is a warning in the build log, not a failure.",
	},
	{
		value: "on",
		text: "Always",
		label: "Always (a failure fails the build)",
		blurb:
			"Checked out every time. A submodule that cannot be fetched fails the build.",
		info: "The repository's token or deploy key is used for submodules on the same host; submodules on other hosts are fetched anonymously over https.",
	},
	{
		value: "off",
		text: "Never",
		label: "Never",
		blurb: "Submodule folders stay empty.",
	},
] as const;

/**
 * One line under the Build control: where the next deployment is built and
 * where its image goes, from the deployment plan. A fallback, an emulated
 * build and a cold cache are alerts under it. Nothing until a plan says.
 */
function BuildPlacementLine({ service }: { service: Service }) {
	const plan = usePlan(service);
	const placement = plan.data?.placement;
	if (!placement) return null;
	return (
		<PlacementReadout
			placement={placement}
			targetName={
				plan.data?.server?.name ?? service.server?.name ?? "its server"
			}
		/>
	);
}

/**
 * Where this service's image is built, and which registry holds it. The
 * default is Inherit: the project's setting, then the team's. A service can
 * pin one build server or build on its own server instead. The line under the
 * control says what the next deployment resolves to. The Dockerfile path is
 * here, with the rarely changed submodule and build context options.
 */
export function BuildSection({
	service,
	draft,
	errors,
	set,
}: DraftProps & { service: Service }) {
	const projects = useQuery(projectsQuery());
	const team = useQuery(teamBuildSettingsQuery());
	const registries = useRegistries();

	const project = projects.data?.find((p) => p.id === service.projectId);
	const projectScope = project
		? toBuildFields(project)
		: { buildLocation: "inherit" as BuildLocation };
	const teamScope = team.data ?? {
		buildLocation: "target" as const,
		registryId: null,
	};

	const effective = resolveBuild({
		service: {
			buildLocation: draft.buildLocation,
			buildServerId: draft.buildServerId,
			registryId: draft.registryId,
		},
		project: projectScope,
		team: teamScope,
	});

	// What an empty registry inherits, named for the select.
	const above = resolveBuild({ project: projectScope, team: teamScope });
	const aboveRegistry = registries.data?.find((r) => r.id === above.registryId);
	const registryParentLabel =
		aboveRegistry && above.registryFrom
			? `${FROM[above.registryFrom]} default: ${aboveRegistry.name}`
			: "none set above";

	const submodules = SUBMODULE_MODES.find((m) => m.value === draft.submodules);

	return (
		<Section
			title="Build"
			description={
				<>
					Where the image is built and which Dockerfile it uses.{" "}
					<DocsLink to="operations/build-servers" />
				</>
			}
		>
			{draft.buildType === "auto" || draft.buildType === "dockerfile" ? (
				<TextRow
					id="set-dockerfile"
					field="config.dockerfilePath"
					label="Dockerfile path"
					description="Relative to the repository root"
					mono
					value={draft.dockerfilePath}
					error={errors.dockerfilePath}
					onChange={(value) => set("dockerfilePath", value)}
				/>
			) : null}
			<SectionBody
				anchor="set-build-location"
				className="flex flex-col gap-3 py-3"
			>
				<LocationControl
					scope="service"
					idPrefix="set-build"
					value={{
						location: draft.buildLocation,
						buildServerId: draft.buildServerId,
						registryId: draft.registryId,
					}}
					onChange={(next) => {
						set("buildLocation", next.location);
						set("buildServerId", next.buildServerId);
						set("registryId", next.registryId);
					}}
					effective={effective}
					registryParentLabel={registryParentLabel}
				/>
				<div data-anchor="build-placement">
					<BuildPlacementLine service={service} />
				</div>
			</SectionBody>
			<MoreOptions
				count={2}
				invalid={Boolean(errors.buildContext || errors.submodules)}
			>
				<SelectRow
					id="set-submodules"
					field="config.submodules"
					label="Git submodules"
					description={submodules?.blurb}
					info={
						submodules && "info" in submodules ? submodules.info : undefined
					}
					value={draft.submodules}
					options={SUBMODULE_MODES}
					onChange={(value) =>
						set("submodules", value as SettingsDraft["submodules"])
					}
				/>
				<TextRow
					id="set-context"
					field="config.buildContext"
					label="Build context"
					description="Relative to the repository root"
					mono
					value={draft.buildContext}
					error={errors.buildContext}
					onChange={(value) => set("buildContext", value)}
				/>
			</MoreOptions>
		</Section>
	);
}
