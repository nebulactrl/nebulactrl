import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { cn } from "cn";
import { Fragment, type ReactNode, useMemo, useState } from "react";
import { errorCode, errorMessage } from "#/api/client-config";
import { updateService } from "#/api/gen/sdk.gen";
import { pendingChangesQuery } from "#/api/hooks";
import { invalidateServices } from "#/api/invalidate";
import type { Service } from "#/api/models";
import {
	type SaveBarState,
	SaveBarStatus,
} from "#/components/common/save-bar-status";
import { Button } from "#/components/ui/button";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { DeployHookSection } from "#/features/deploy-hook";
import { MaintenanceSection } from "#/features/maintenance";
import { ProcessesSection } from "#/features/processes/processes-section";
import { ConfigServiceProvider } from "#/features/project-config";
import {
	checkBlocker,
	isStack,
	stackComponents,
} from "#/features/stacks/model";
import { useStackCheck } from "#/features/stacks/queries";
import {
	ComposeSection,
	StackDeploySection,
	StackLimitsSection,
	StackNetworkingSection,
} from "#/features/stacks/settings-sections";
import { TasksSection } from "#/features/tasks";
import { TemplateOrigin } from "#/features/templates/template-origin";
import { MonitorSection } from "#/features/uptime";
import { BuildSection } from "./build-section";
import { DangerZone } from "./danger-zone";
import { dockerErrorMessage, REQUIRES_UNRESTRICTED } from "./docker-options";
import { DockerOptionsSection } from "./docker-options-section";
import { useDockerPolicy } from "./docker-policy";
import {
	buildPatch,
	type SettingsDraft,
	toDraft,
	validateDraft,
} from "./draft";
import { CategoryHeadProvider, type DraftErrors, Section } from "./fields";
import { FilesSection } from "./files-section";
import {
	CommandsSection,
	HealthSection,
	ReleasesSection,
} from "./health-deploy-sections";
import { MeshSection } from "./mesh-section";
import { NetworkingSection } from "./networking-section";
import { OneShotRow } from "./oneshot-row";
import { PlacementSection, VolumesSection } from "./placement-volumes-sections";
import { ScalingSection } from "./scaling-section";
import { saveBarVisible } from "./settings-chrome";
import { SettingsPages } from "./settings-nav";
import {
	type CategoryId,
	categoriesFor,
	defaultCategory,
	editedCategories,
	firstInvalidCategory,
	invalidCategories,
	resolveCategory,
	type SectionId,
	type SettingsSubject,
	stagedCounts,
} from "./settings-nav-model";
import { NameRow, ResourcesSection, SourceSection } from "./source-section";
import { tailnetErrorMessage } from "./tailnet-errors";
import { TailnetSection } from "./tailnet-section";

export interface SettingsTabProps {
	service: Service;
	/** `?section=` of the address: a category, or a section inside one. */
	section: string | undefined;
	onSectionChange(section: CategoryId): void;
}

/**
 * Everything about a service that is configuration, one category at a time
 * (General, Deploy, Networking, Resources, Storage, Operations, Advanced,
 * Danger zone). Fields
 * edit a working copy shared by every category; Save sends one PATCH with
 * `If-Match` set to the generation the form was opened on, so two people
 * editing never overwrite each other silently. Saved changes are staged: they
 * reach the container on the next deploy.
 */
export function SettingsTab({
	service,
	section,
	onSectionChange,
}: SettingsTabProps) {
	const queryClient = useQueryClient();
	const { policy: dockerPolicy } = useDockerPolicy();
	// `null` means untouched: the form follows the service as it refetches.
	const [edited, setEdited] = useState<SettingsDraft | null>(null);
	const baseline = useMemo(() => toDraft(service), [service]);
	const draft = edited ?? baseline;
	const stack = isStack(service);
	// A stack's compose text is judged live; the check only blocks a text that was edited.
	const stackCheck = useStackCheck({
		environmentId: service.environmentId,
		compose: draft.stackCompose,
		serverId: draft.serverId,
		enabled: stack,
	});
	const draftErrors = useMemo(
		() => validateDraft(draft, service.sourceType, dockerPolicy),
		[draft, service.sourceType, dockerPolicy],
	);
	const errors: DraftErrors = useMemo(() => {
		if (!stack || draftErrors.stackCompose) return draftErrors;
		if (draft.stackCompose === baseline.stackCompose) return draftErrors;
		const blocker = stackCheck.pending ? null : checkBlocker(stackCheck);
		return blocker ? { ...draftErrors, stackCompose: blocker } : draftErrors;
	}, [
		stack,
		draftErrors,
		draft.stackCompose,
		baseline.stackCompose,
		stackCheck,
	]);
	// The components the form shows limits and domains for: the edited file's, once checked.
	const liveComponents = useMemo(
		() =>
			draft.stackCompose !== baseline.stackCompose && stackCheck.result
				? stackCheck.result.components
				: stackComponents(service),
		[draft.stackCompose, baseline.stackCompose, stackCheck.result, service],
	);
	const errorKeys = useMemo(() => Object.keys(errors), [errors]);
	const invalid = errorKeys.length > 0;
	const patch = useMemo(
		() => (edited ? buildPatch(baseline, edited, service.sourceType) : null),
		[baseline, edited, service.sourceType],
	);

	const set = <K extends keyof SettingsDraft>(
		key: K,
		value: SettingsDraft[K],
	) => setEdited((current) => ({ ...(current ?? baseline), [key]: value }));

	const { kind, sourceType } = service;
	const subject = useMemo<SettingsSubject>(
		() => ({ kind, sourceType, oneshot: draft.oneshot }),
		[kind, sourceType, draft.oneshot],
	);
	const categories = useMemo(() => categoriesFor(subject), [subject]);
	const active =
		resolveCategory(section, categories) ?? defaultCategory(categories);
	const activeCategory = categories.find((category) => category.id === active);
	const editedIds = useMemo(
		() => editedCategories(baseline, draft, service.sourceType),
		[baseline, draft, service.sourceType],
	);
	const invalidIds = useMemo(() => invalidCategories(errorKeys), [errorKeys]);
	const stagedChanges = useQuery({
		...pendingChangesQuery(service.id),
		enabled: service.pendingChangeCounts.settings > 0,
	});
	const staged = useMemo(
		() =>
			stagedCounts(
				service.pendingChangeCounts.settings > 0
					? (stagedChanges.data ?? []).map((change) => change.field)
					: [],
			),
		[service.pendingChangeCounts.settings, stagedChanges.data],
	);

	const save = useMutation({
		mutationFn: async () => {
			if (!patch) return;
			await updateService({
				path: { serviceId: service.id },
				headers: { "If-Match": `"${service.generation}"` },
				body: patch,
			});
		},
		onSuccess: async () => {
			await invalidateServices(queryClient);
			setEdited(null);
			toast.success("Settings saved", "Staged until the next deploy.");
		},
		onError: (error) => {
			// A lost race: the global handler has toasted and refetched; the
			// stale draft would only fail again, so start from what is there now.
			if (errorCode(error) === "generation_conflict") {
				setEdited(null);
				return;
			}
			// Otherwise land where the problem is, not on a category that is fine.
			const problem =
				firstInvalidCategory(errorKeys, categories) ??
				(errorCode(error) === "process_in_use" ? "deploy" : undefined) ??
				(tailnetErrorMessage(error) !== undefined ? "networking" : undefined) ??
				(dockerErrorMessage(error) !== undefined ? "advanced" : undefined);
			if (problem && categories.some((category) => category.id === problem)) {
				onSectionChange(problem);
			}
		},
	});

	const dirty = patch !== null;
	const saveFailed =
		save.isError && errorCode(save.error) !== "generation_conflict";
	const dockerServerError = saveFailed
		? dockerErrorMessage(save.error)
		: undefined;
	// The tier refusing the chosen server is a placement problem too.
	const placementError =
		errorCode(save.error) === REQUIRES_UNRESTRICTED
			? dockerServerError
			: undefined;
	const barState: SaveBarState = saveFailed
		? "failed"
		: dirty
			? invalid
				? "invalid"
				: "dirty"
			: "saved";
	const firstProblem = invalid
		? firstInvalidCategory(errorKeys, categories)
		: undefined;
	const problemLabel = categories.find(
		(category) => category.id === firstProblem,
	)?.label;

	const sectionBody = (id: SectionId): ReactNode => {
		switch (id) {
			case "identity":
				return (
					<Section title="Identity">
						<NameRow draft={draft} errors={errors} set={set} />
						{isStack(service) ? null : <OneShotRow draft={draft} set={set} />}
					</Section>
				);
			case "source":
				return (
					<SourceSection
						service={service}
						dirty={dirty}
						draft={draft}
						errors={errors}
						set={set}
					/>
				);
			case "build":
				return (
					<BuildSection
						service={service}
						draft={draft}
						errors={errors}
						set={set}
					/>
				);
			case "deploy":
				return (
					<CommandsSection
						service={service}
						draft={draft}
						errors={errors}
						set={set}
					/>
				);
			case "processes":
				return (
					<ProcessesSection
						service={service}
						draft={draft}
						errors={errors}
						set={set}
					/>
				);
			case "releases":
				return (
					<ReleasesSection
						service={service}
						draft={draft}
						errors={errors}
						set={set}
					/>
				);
			case "deploy-hook":
				return (
					<DeployHookSection
						serviceId={service.id}
						serviceName={service.name}
					/>
				);
			case "networking":
				return (
					<NetworkingSection
						service={service}
						draft={draft}
						errors={errors}
						set={set}
					/>
				);
			case "mesh":
				return <MeshSection service={service} />;
			case "tailnet":
				return (
					<TailnetSection
						service={service}
						draft={draft}
						errors={errors}
						set={set}
					/>
				);
			case "health-check":
				return <HealthSection draft={draft} errors={errors} set={set} />;
			case "uptime-monitoring":
				return <MonitorSection service={service} />;
			case "scaling":
				return <ScalingSection service={service} />;
			case "resources":
				return <ResourcesSection draft={draft} errors={errors} set={set} />;
			case "docker-options":
				return (
					<DockerOptionsSection
						draft={draft}
						service={service}
						errors={errors}
						set={set}
						serverError={dockerServerError}
					/>
				);
			case "placement":
				return (
					<PlacementSection
						serverError={placementError}
						service={service}
						draft={draft}
						errors={errors}
						set={set}
					/>
				);
			case "volumes":
				return <VolumesSection service={service} />;
			case "files":
				return <FilesSection draft={draft} errors={errors} set={set} />;
			case "tasks":
				return <TasksSection service={service} />;
			case "maintenance":
				return <MaintenanceSection service={service} />;
			case "danger-zone":
				return <DangerZone service={service} />;
			case "compose":
				return (
					<ComposeSection
						draft={draft}
						errors={errors}
						set={set}
						check={stackCheck}
						serverName={
							draft.serverId === (service.serverId ?? "")
								? service.server?.name
								: undefined
						}
					/>
				);
			case "stack-deploy":
				return <StackDeploySection draft={draft} errors={errors} set={set} />;
			case "stack-limits":
				return (
					<StackLimitsSection
						service={service}
						draft={draft}
						errors={errors}
						set={set}
						components={liveComponents}
					/>
				);
			case "stack-networking":
				return (
					<StackNetworkingSection
						service={service}
						draft={draft}
						errors={errors}
						set={set}
						components={liveComponents}
					/>
				);
			default:
				// The route-only sections belong to the route's own Settings.
				return null;
		}
	};

	return (
		<SettingsPages
			serviceId={service.id}
			subject={subject}
			categories={categories}
			active={active}
			onSelect={onSectionChange}
			edited={editedIds}
			invalid={invalidIds}
			staged={staged}
			footer={
				// Operations and the Danger zone act at once, and Resources opens on
				// Scaling's own Apply: with nothing edited the bar has no job there.
				activeCategory &&
				saveBarVisible({ category: activeCategory, dirty, saveFailed }) ? (
					<div
						className="surface-float sticky bottom-0 z-10 flex items-center gap-3 rounded-none border-line border-x-0 border-b-0 px-4 py-2.5"
						aria-live="polite"
					>
						{barState === "saved" ? (
							// At rest nothing happened, so there is no marker: words only.
							<span className="mr-auto text-ink-3 text-xs">
								All changes saved
							</span>
						) : (
							<SaveBarStatus state={barState}>
								{saveFailed
									? (tailnetErrorMessage(save.error) ??
										dockerErrorMessage(save.error) ??
										errorMessage(save.error))
									: invalid
										? "Fix the highlighted fields to save."
										: "Unsaved changes"}
							</SaveBarStatus>
						)}
						{firstProblem && firstProblem !== active ? (
							<Button
								variant="ghost"
								onClick={() => onSectionChange(firstProblem)}
							>
								Show in {problemLabel}
							</Button>
						) : null}
						<Button
							variant="secondary"
							disabled={!dirty || save.isPending}
							onClick={() => setEdited(null)}
						>
							Discard
						</Button>
						<Button
							variant="primary"
							disabled={!dirty || invalid || save.isPending}
							onClick={() => save.mutate()}
						>
							{save.isPending ? <Spinner /> : null}
							Save changes
						</Button>
					</div>
				) : null
			}
		>
			{/* Every category stays mounted and only the active one is shown, so
			    what was typed into a form in another category survives the switch. */}
			<ConfigServiceProvider serviceId={service.id}>
				{categories.map((category) => (
					<div
						key={category.id}
						className={cn(
							"flex flex-col gap-4",
							category.id !== active && "hidden",
						)}
					>
						{category.id === "general" ? (
							<TemplateOrigin serviceId={service.id} />
						) : null}
						<CategoryHeadProvider category={category}>
							{category.sections
								.map(({ id }) => ({ id, body: sectionBody(id) }))
								.filter(({ body }) => body !== null)
								.map(({ id, body }) => (
									<Fragment key={id}>{body}</Fragment>
								))}
						</CategoryHeadProvider>
					</div>
				))}
			</ConfigServiceProvider>
		</SettingsPages>
	);
}
