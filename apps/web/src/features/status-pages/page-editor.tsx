import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeftIcon, PlusIcon, Trash2Icon } from "lucide-react";
import { useMemo, useRef, useState } from "react";
import { errorCode, errorMessage, errorStatus } from "#/api/client-config";
import {
	deleteStatusPage,
	setStatusPageComponents,
	updateStatusPage,
} from "#/api/gen/sdk.gen";
import type { StatusPageView } from "#/api/gen/types.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { ErrorState } from "#/components/common/error-state";
import {
	type SaveBarState,
	SaveBarStatus,
} from "#/components/common/save-bar-status";
import { PageFrame } from "#/components/shell/page-frame";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { Section, SectionBody } from "#/features/service-settings/fields";
import { useCurrentTeam } from "#/features/teams/queries";
import { canManageMembers } from "#/features/teams/roles";
import { count } from "#/lib/format";
import { useDocumentTitle } from "#/lib/hooks/use-document-title";
import { StateLabel } from "./badges";
import { Note } from "./block";
import { ComponentsEditor } from "./components-editor";
import { IncidentDialog } from "./incident-dialog";
import { MaintenanceDialog } from "./maintenance-dialog";
import {
	type ComponentRow,
	compareIncidents,
	componentsBody,
	formFromPage,
	groupWindows,
	hasErrors,
	type PageForm,
	type PageFormErrors,
	type PageProblem,
	type ProblemField,
	pageFormDirty,
	pageOverall,
	pagePatch,
	previewBlocker,
	rowProblems,
	rowsDiffer,
	rowsFromPage,
	savePageProblem,
	validatePageForm,
} from "./model";
import {
	GeneralSection,
	HostnameSection,
	PublishSection,
} from "./page-sections";
import { PreviewAction } from "./preview-action";
import {
	invalidateStatusPages,
	maintenanceWindowsQuery,
	statusIncidentsQuery,
	statusPageQuery,
	useServiceStates,
} from "./queries";
import { IncidentRows, MaintenanceRows } from "./rows";
import { useMaintenanceActions } from "./use-maintenance-actions";

type Step = "components" | "form";

/** The incidents and maintenance that mention this page, and the buttons to add them. */
function PageActivity({
	page,
	canManage,
}: {
	page: StatusPageView;
	canManage: boolean;
}) {
	const incidents = useQuery(statusIncidentsQuery({ pageId: page.id }));
	const windows = useQuery(maintenanceWindowsQuery({ pageId: page.id }));
	const actions = useMaintenanceActions();
	const [posting, setPosting] = useState(false);
	const [scheduling, setScheduling] = useState(false);
	const pageIds = useMemo(() => [page.id], [page.id]);
	const titles = useMemo(() => new Map([[page.id, page.title]]), [page]);

	const sorted = [...(incidents.data ?? [])].sort(compareIncidents);
	const groups = groupWindows(windows.data ?? []);
	const current = [...groups.active, ...groups.upcoming];

	return (
		<>
			<Section
				title="Incidents"
				description="Incidents are public statements: what you write here is shown as written, on this page, without signing in."
				aside={
					canManage ? (
						<Button
							type="button"
							variant="secondary"
							size="sm"
							onClick={() => setPosting(true)}
						>
							<PlusIcon />
							Post incident
						</Button>
					) : null
				}
			>
				<SectionBody className="py-1">
					{incidents.isPending ? (
						<Skeleton className="h-16 rounded-card" />
					) : sorted.length === 0 ? (
						<Note>
							No incidents posted to this page. When something breaks, post one
							here so visitors hear it from you first.
						</Note>
					) : (
						<IncidentRows
							label="Incidents on this page"
							incidents={sorted}
							pageTitles={titles}
						/>
					)}
				</SectionBody>
			</Section>

			<Section
				title="Maintenance"
				description="Planned work this page announces. Times are shown in your time zone, with UTC beneath."
				aside={
					canManage ? (
						<Button
							type="button"
							variant="secondary"
							size="sm"
							onClick={() => setScheduling(true)}
						>
							<PlusIcon />
							Schedule maintenance
						</Button>
					) : null
				}
			>
				<SectionBody className="py-1">
					{windows.isPending ? (
						<Skeleton className="h-16 rounded-card" />
					) : current.length === 0 && groups.past.length === 0 ? (
						<Note>
							No maintenance is scheduled for this page. Services in a window do
							not raise outage alerts, and their checks are left out of
							availability.
						</Note>
					) : (
						<div className="flex flex-col gap-2">
							{current.length > 0 ? (
								<MaintenanceRows
									label="Active and upcoming maintenance"
									windows={current}
									pageTitles={titles}
									canManage={canManage}
									onEdit={actions.onEdit}
									onCancel={actions.onCancel}
									onDelete={actions.onDelete}
								/>
							) : (
								<Note>Nothing running or coming up.</Note>
							)}
							{groups.past.length > 0 ? (
								<details className="group/past">
									<summary className="cursor-pointer select-none rounded-sm text-ink-2 text-xs outline-none hover:text-ink-1 focus-visible:focus-ring">
										{count(groups.past.length, "past window")}
									</summary>
									<MaintenanceRows
										label="Past maintenance"
										windows={groups.past}
										pageTitles={titles}
										canManage={canManage}
										onEdit={actions.onEdit}
										onCancel={actions.onCancel}
										onDelete={actions.onDelete}
									/>
								</details>
							) : null}
						</div>
					)}
				</SectionBody>
			</Section>

			<IncidentDialog
				open={posting}
				onOpenChange={setPosting}
				incident={null}
				defaultPageIds={pageIds}
			/>
			<MaintenanceDialog
				open={scheduling}
				onOpenChange={setScheduling}
				maintenance={null}
				defaultPageIds={pageIds}
			/>
			<MaintenanceDialog
				open={actions.editing !== null}
				onOpenChange={(open) => (open ? null : actions.closeEditor())}
				maintenance={actions.editing}
			/>
		</>
	);
}

function EditorBody({ page }: { page: StatusPageView }) {
	const queryClient = useQueryClient();
	const navigate = useNavigate();
	const confirm = useConfirm();
	const { role } = useCurrentTeam();
	const canManage = canManageMembers(role);
	useDocumentTitle([page.title, "Status pages"]);

	const saved = useMemo(() => formFromPage(page), [page]);
	const baseline = useMemo(() => rowsFromPage(page.components), [page]);
	const [formEdit, setFormEdit] = useState<PageForm | null>(null);
	const [rowsEdit, setRowsEdit] = useState<ComponentRow[] | null>(null);
	const form = formEdit ?? saved;
	const rows = rowsEdit ?? baseline;

	const [serverProblems, setServerProblems] = useState<
		Partial<Record<ProblemField, string>>
	>({});
	const [saveError, setSaveError] = useState<string | null>(null);
	const [componentsError, setComponentsError] = useState<string | null>(null);
	const [publishError, setPublishError] = useState<string | null>(null);
	const step = useRef<Step>("form");

	const formDirty = formEdit !== null && pageFormDirty(formEdit, saved);
	const rowsDirty = rowsEdit !== null && rowsDiffer(rowsEdit, baseline);
	const dirty = formDirty || rowsDirty;
	const formErrors = validatePageForm(form, "edit");
	const problems = rowProblems(rows);
	const invalid = hasErrors(formErrors) || hasErrors(problems);
	// What the control plane refused sits at its field until the field changes.
	const shownErrors: PageFormErrors = { ...formErrors };
	for (const field of ["title", "slug", "hostname"] as const) {
		const message = serverProblems[field];
		if (message && !shownErrors[field]) shownErrors[field] = message;
	}

	const editForm = (change: Partial<PageForm>) => {
		setFormEdit({ ...form, ...change });
		setServerProblems({});
		setSaveError(null);
	};
	const editRows = (next: ComponentRow[]) => {
		setRowsEdit(next);
		setComponentsError(null);
		setSaveError(null);
	};
	const discard = () => {
		setFormEdit(null);
		setRowsEdit(null);
		setServerProblems({});
		setSaveError(null);
		setComponentsError(null);
	};

	const save = useMutation({
		mutationFn: async () => {
			if (rowsDirty) {
				step.current = "components";
				await setStatusPageComponents({
					path: { pageId: page.id },
					body: { components: componentsBody(rows) },
					throwOnError: true,
				});
				await invalidateStatusPages(queryClient);
				setRowsEdit(null);
			}
			if (formDirty) {
				step.current = "form";
				await updateStatusPage({
					path: { pageId: page.id },
					body: pagePatch(form, saved),
					throwOnError: true,
				});
				await invalidateStatusPages(queryClient);
				setFormEdit(null);
			}
		},
		onSuccess: () => {
			setServerProblems({});
			setSaveError(null);
			setComponentsError(null);
			toast.success(
				"Status page saved",
				"The public page shows the change within moments.",
			);
		},
		onError: (e) => {
			const problem: PageProblem | null = savePageProblem(
				errorCode(e),
				page.published,
			);
			if (step.current === "components") {
				setComponentsError(problem?.message ?? errorMessage(e));
			} else if (
				problem &&
				problem.field !== "publish" &&
				problem.field !== "description" &&
				problem.field !== "footer"
			) {
				setServerProblems({ [problem.field]: problem.message });
			} else {
				setSaveError(problem?.message ?? errorMessage(e));
			}
		},
	});

	const publish = useMutation({
		mutationFn: (published: boolean) =>
			updateStatusPage({
				path: { pageId: page.id },
				body: { published },
				throwOnError: true,
			}),
		onSuccess: async (_, published) => {
			setPublishError(null);
			await invalidateStatusPages(queryClient);
			toast.success(
				published ? "Page published" : "Page unpublished",
				published
					? "It is served on its hostname within moments."
					: "It is taken down within moments.",
			);
		},
		onError: (e) =>
			setPublishError(
				savePageProblem(errorCode(e), page.published)?.message ??
					errorMessage(e),
			),
	});

	const remove = useMutation({
		mutationFn: () =>
			deleteStatusPage({ path: { pageId: page.id }, throwOnError: true }),
		onSuccess: async () => {
			await invalidateStatusPages(queryClient);
			toast.success(
				"Status page deleted",
				"Its hostname stops serving it. Incidents and maintenance stay.",
			);
			void navigate({ to: "/status-pages" });
		},
	});

	const incidents = useQuery(statusIncidentsQuery({ status: "active" }));
	const windows = useQuery(maintenanceWindowsQuery({ phase: "active" }));
	const { states } = useServiceStates();
	const overall = pageOverall(
		page,
		states,
		incidents.data ?? [],
		windows.data ?? [],
	);

	const barState: SaveBarState = saveError
		? "failed"
		: dirty
			? invalid
				? "invalid"
				: "dirty"
			: "saved";

	const hostname = page.hostname;
	const blocker = previewBlocker(page);
	const [explaining, setExplaining] = useState(false);

	return (
		<PageFrame
			page="status pages"
			title={page.title}
			description={
				<span className="flex flex-wrap items-center gap-x-3 gap-y-1">
					<StateLabel state={overall} />
					<span className="font-mono text-xs">
						{hostname ?? "No hostname yet"}
					</span>
					<span>{page.published ? "Published" : "Draft"}</span>
					<span>{count((page.components ?? []).length, "component")}</span>
				</span>
			}
			actions={
				<div className="flex flex-wrap items-center gap-2">
					<Button
						variant="ghost"
						nativeButton={false}
						render={<Link to="/status-pages" />}
					>
						<ArrowLeftIcon />
						All status pages
					</Button>
					<PreviewAction
						page={page}
						explaining={explaining}
						onExplain={() => setExplaining((on) => !on)}
					/>
				</div>
			}
		>
			{explaining && blocker ? (
				<Note>
					<span role="status">{blocker}</span>
				</Note>
			) : null}

			{canManage ? null : (
				<Note>
					Only team owners and admins change status pages. You can read this
					one, and its incidents and maintenance, but not edit it.
				</Note>
			)}

			<fieldset
				disabled={!canManage}
				className="m-0 flex min-w-0 flex-col gap-8 border-0 p-0"
			>
				<GeneralSection
					form={form}
					errors={shownErrors}
					onChange={editForm}
					readOnly={!canManage}
				/>
				<HostnameSection
					page={page}
					form={form}
					error={shownErrors.hostname}
					onChange={editForm}
				/>
				<PublishSection
					page={page}
					dirty={dirty}
					pending={publish.isPending}
					problem={publishError ?? undefined}
					readOnly={!canManage}
					onChange={(published) => {
						setPublishError(null);
						publish.mutate(published);
					}}
				/>
				<Section
					title="Components"
					description="The services visitors see, each under a public name. Only the name, state and history reach the public page: never the service's own name, project or environment."
				>
					<SectionBody className="py-2">
						<ComponentsEditor
							pageId={page.id}
							rows={rows}
							onChange={editRows}
						/>
						{componentsError ? (
							<FieldError className="mt-3">{componentsError}</FieldError>
						) : null}
					</SectionBody>
				</Section>
			</fieldset>

			<PageActivity page={page} canManage={canManage} />

			{canManage ? (
				<Section
					tone="danger"
					title="Delete page"
					description="Deletes the page and takes its hostname down. Incidents and maintenance posted to it stay, and show on any other page they are posted to."
				>
					<SectionBody className="py-2">
						<Button
							type="button"
							variant="danger"
							onClick={() =>
								void confirm({
									title: `Delete “${page.title}”?`,
									body: `${page.published && hostname ? `${hostname} stops serving it within moments. ` : ""}Its components and settings are removed. This cannot be undone.`,
									cta: "Delete page",
									danger: true,
									hold: false,
									onConfirm: async () => {
										await remove.mutateAsync();
									},
								})
							}
						>
							<Trash2Icon />
							Delete page
						</Button>
					</SectionBody>
				</Section>
			) : null}

			{canManage ? (
				<div className="sticky bottom-3 z-10 h-10">
					<fieldset
						aria-label="Save changes"
						aria-live="polite"
						className={`surface-float absolute inset-x-0 bottom-0 m-0 flex min-w-0 items-center gap-2 rounded-card border border-line px-3 py-2 transition-[opacity,translate,visibility] duration-quick ease-out motion-reduce:transition-none sm:gap-3 sm:px-4 sm:py-2.5 ${dirty ? "visible translate-y-0 opacity-100" : "invisible translate-y-2 opacity-0"}`}
					>
						<SaveBarStatus state={barState}>
							{saveError
								? saveError
								: invalid
									? "Fix the highlighted fields to save."
									: "Unsaved changes"}
						</SaveBarStatus>
						<Button
							type="button"
							variant="secondary"
							disabled={!dirty || save.isPending}
							onClick={discard}
						>
							Discard
						</Button>
						<Button
							type="button"
							variant="primary"
							disabled={!dirty || invalid || save.isPending}
							onClick={() => {
								setSaveError(null);
								save.mutate();
							}}
						>
							{save.isPending ? <Spinner /> : null}
							Save changes
						</Button>
					</fieldset>
				</div>
			) : null}
		</PageFrame>
	);
}

/**
 * The editor of one status page: the form (general, hostname with its DNS
 * check, publish), the components, the incidents and maintenance posted to it.
 * One sticky bar saves or discards the form and the components together;
 * publishing is its own switch because it acts on what is saved.
 */
export function PageEditor({ pageId }: { pageId: string }) {
	const query = useQuery(statusPageQuery(pageId));
	const back = (
		<Button
			variant="ghost"
			nativeButton={false}
			render={<Link to="/status-pages" />}
		>
			<ArrowLeftIcon />
			All status pages
		</Button>
	);

	if (query.isPending) {
		return (
			<PageFrame page="status pages" title="Status page" actions={back}>
				<div
					className="flex flex-col gap-4"
					role="status"
					aria-label="Loading status page"
				>
					<Skeleton className="h-56 rounded-card" />
					<Skeleton className="h-40 rounded-card" />
					<Skeleton className="h-72 rounded-card" />
				</div>
			</PageFrame>
		);
	}
	if (!query.data) {
		const missing = errorStatus(query.error) === 404;
		return (
			<PageFrame page="status pages" title="Status page" actions={back}>
				<ErrorState
					frame="raised"
					title={
						missing
							? "This status page does not exist"
							: "The page did not load"
					}
					body={
						missing
							? "It may have been deleted. Nothing was changed."
							: "The control plane did not answer. Nothing was changed; try again."
					}
					{...(missing ? {} : { retry: () => void query.refetch() })}
				/>
			</PageFrame>
		);
	}
	return <EditorBody key={query.data.id} page={query.data} />;
}
