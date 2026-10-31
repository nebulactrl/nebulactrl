import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeftIcon, PencilIcon, Trash2Icon } from "lucide-react";
import { type FormEvent, useRef, useState } from "react";
import { errorMessage, errorStatus } from "#/api/client-config";
import {
	deleteStatusIncident,
	postStatusIncidentUpdate,
} from "#/api/gen/sdk.gen";
import type { StatusIncidentView } from "#/api/gen/types.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
import { PageFrame } from "#/components/shell/page-frame";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import {
	Field,
	FieldDescription,
	FieldError,
	FieldLabel,
} from "#/components/ui/field";
import { Segmented } from "#/components/ui/segmented";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { Textarea } from "#/components/ui/textarea";
import { toast } from "#/components/ui/toast";
import { useTeamServices } from "#/features/log-drains/use-team-services";
import { useCurrentTeam } from "#/features/teams/queries";
import { canManageMembers } from "#/features/teams/roles";
import { count } from "#/lib/format";
import { useDocumentTitle } from "#/lib/hooks/use-document-title";
import { ImpactBadge, IncidentStatusBadge } from "./badges";
import { Block, Note } from "./block";
import { IncidentDialog } from "./incident-dialog";
import {
	type IncidentStatus,
	isOpen,
	MAX_UPDATE_BODY,
	OPEN_INCIDENT_STATUSES,
	STATUS_HINT,
	STATUS_LABEL,
	suggestedStatus,
	timelineOf,
	validateUpdateBody,
} from "./model";
import {
	invalidateStatusPages,
	statusIncidentQuery,
	statusPagesQuery,
} from "./queries";
import { LocalTime } from "./rows";

const RESOLVED_TEXT = "This incident has been resolved.";

function PostUpdate({ incident }: { incident: StatusIncidentView }) {
	const queryClient = useQueryClient();
	const bodyRef = useRef<HTMLTextAreaElement>(null);
	const [picked, setPicked] = useState<IncidentStatus | null>(null);
	const [body, setBody] = useState("");
	const [touched, setTouched] = useState(false);
	const [error, setError] = useState<string | null>(null);

	const status = picked ?? suggestedStatus(incident.status);
	const problem = validateUpdateBody(body);
	const reopening = incident.status === "resolved" && status !== "resolved";

	const post = useMutation({
		mutationFn: () =>
			postStatusIncidentUpdate({
				path: { incidentId: incident.id },
				body: { status, body: body.trim() },
				throwOnError: true,
			}),
		onSuccess: async () => {
			await invalidateStatusPages(queryClient);
			toast.success(
				status === "resolved" ? "Incident resolved" : "Update posted",
				status === "resolved"
					? "The pages return to their live state."
					: "It is on the pages now.",
			);
			setBody("");
			setPicked(null);
			setTouched(false);
			setError(null);
		},
		onError: (e) => setError(errorMessage(e)),
	});

	const submit = (event: FormEvent) => {
		event.preventDefault();
		setTouched(true);
		if (problem || post.isPending) return;
		setError(null);
		post.mutate();
	};

	const resolve = () => {
		setPicked("resolved");
		if (body.trim() === "") setBody(RESOLVED_TEXT);
		bodyRef.current?.focus();
	};

	return (
		<form onSubmit={submit} className="flex flex-col gap-3">
			<Field>
				<FieldLabel id="update-status-label">Status</FieldLabel>
				<div>
					<Segmented<IncidentStatus>
						aria-label="Status of the new update"
						font="sans"
						density="regular"
						className="max-w-full flex-wrap"
						value={status}
						onValueChange={setPicked}
						options={OPEN_INCIDENT_STATUSES.map((value) => ({
							value,
							label: STATUS_LABEL[value],
						}))}
					/>
				</div>
				<FieldDescription>
					{status === "resolved"
						? `${STATUS_HINT.resolved} Pick a status above to keep it open.`
						: reopening
							? "This incident is resolved. An update with any other status reopens it."
							: STATUS_HINT[status]}
				</FieldDescription>
			</Field>
			<Field>
				<FieldLabel htmlFor="update-body">What is new</FieldLabel>
				<Textarea
					id="update-body"
					ref={bodyRef}
					rows={4}
					maxLength={MAX_UPDATE_BODY + 100}
					placeholder="We found the cause and are rolling out a fix."
					aria-invalid={touched && problem !== null}
					value={body}
					onChange={(event) => setBody(event.target.value)}
				/>
				<FieldDescription>
					Plain text; line breaks stay. An update cannot be edited or removed
					once it is posted.
				</FieldDescription>
				{touched && problem ? <FieldError>{problem}</FieldError> : null}
			</Field>
			{error ? <FieldError>{error}</FieldError> : null}
			<div className="flex flex-wrap items-center gap-2">
				<Button
					type="submit"
					variant={problem ? "secondary" : "primary"}
					disabled={post.isPending}
				>
					{post.isPending ? <Spinner /> : null}
					{status === "resolved" ? "Post resolution" : "Post update"}
				</Button>
				{isOpen(incident) && status !== "resolved" ? (
					<Button type="button" variant="secondary" onClick={resolve}>
						Resolve
					</Button>
				) : null}
			</div>
		</form>
	);
}

function Timeline({ incident }: { incident: StatusIncidentView }) {
	const updates = timelineOf(incident);
	if (updates.length === 0) {
		return <Note>No updates yet.</Note>;
	}
	return (
		<ol
			aria-label="Timeline, newest first"
			className="m-0 flex list-none flex-col divide-y divide-line-faint p-0"
		>
			{updates.map((update, index) => (
				<li key={update.id} className="flex flex-col gap-1.5 py-3">
					<div className="flex flex-wrap items-center gap-2 text-ink-3 text-xs">
						<IncidentStatusBadge status={update.status} />
						<LocalTime iso={update.createdAt} />
						{index === 0 ? <span>Latest</span> : null}
					</div>
					<p className="m-0 whitespace-pre-wrap text-ink-1 text-sm leading-relaxed">
						{update.body}
					</p>
				</li>
			))}
		</ol>
	);
}

/**
 * One incident: its timeline, a form to post an update (with a status change,
 * or to resolve it), and where it is posted. Editing the title, impact, pages
 * and services opens the same dialog that opened it.
 */
export function IncidentDetail({ incidentId }: { incidentId: string }) {
	const queryClient = useQueryClient();
	const navigate = useNavigate();
	const confirm = useConfirm();
	const { role } = useCurrentTeam();
	const canManage = canManageMembers(role);
	const incident = useQuery(statusIncidentQuery(incidentId));
	const pages = useQuery(statusPagesQuery());
	const team = useTeamServices(true);
	const [editing, setEditing] = useState(false);
	const data = incident.data;
	useDocumentTitle([data?.title, "Incident"]);

	const remove = useMutation({
		mutationFn: () =>
			deleteStatusIncident({ path: { incidentId }, throwOnError: true }),
		onSuccess: async () => {
			await invalidateStatusPages(queryClient);
			toast.success("Incident deleted", "It is gone from every page.");
			void navigate({ to: "/status-pages" });
		},
	});

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

	if (incident.isPending) {
		return (
			<PageFrame page="status pages" title="Incident" actions={back}>
				<div
					className="flex flex-col gap-4"
					role="status"
					aria-label="Loading incident"
				>
					<Skeleton className="h-32 rounded-card" />
					<Skeleton className="h-64 rounded-card" />
				</div>
			</PageFrame>
		);
	}
	if (!data) {
		const missing = errorStatus(incident.error) === 404;
		return (
			<PageFrame page="status pages" title="Incident" actions={back}>
				<ErrorState
					frame="raised"
					title={
						missing
							? "This incident does not exist"
							: "The incident did not load"
					}
					body={
						missing
							? "It may have been deleted. Nothing was changed."
							: "The control plane did not answer. Nothing was changed; try again."
					}
					{...(missing ? {} : { retry: () => void incident.refetch() })}
				/>
			</PageFrame>
		);
	}

	const pageById = new Map((pages.data ?? []).map((p) => [p.id, p]));
	const serviceNames = new Map(team.services.map((s) => [s.id, s]));
	const posted = data.pageIds ?? [];
	const affected = data.serviceIds ?? [];

	return (
		<PageFrame
			page="status pages"
			title={data.title}
			description={
				<span className="flex flex-wrap items-center gap-2">
					<ImpactBadge impact={data.impact} />
					<IncidentStatusBadge status={data.status} />
					<span>
						Opened <Relative iso={data.createdAt} />
						{data.resolvedAt ? (
							<>
								, resolved <Relative iso={data.resolvedAt} />
							</>
						) : null}
					</span>
				</span>
			}
			actions={
				<div className="flex flex-wrap items-center gap-2">
					{back}
					{canManage ? (
						<>
							<Button variant="secondary" onClick={() => setEditing(true)}>
								<PencilIcon />
								Edit
							</Button>
							<Button
								variant="danger"
								onClick={() =>
									void confirm({
										title: `Delete “${data.title}”?`,
										body: "The incident and its whole timeline disappear from every page it is posted to. This cannot be undone. To end an incident instead, post a resolving update.",
										cta: "Delete incident",
										danger: true,
										hold: false,
										onConfirm: async () => {
											await remove.mutateAsync();
										},
									})
								}
							>
								<Trash2Icon />
								Delete
							</Button>
						</>
					) : null}
				</div>
			}
		>
			{posted.length === 0 ? (
				<Alert variant="info">
					<AlertTitle>Not posted to any page</AlertTitle>
					<AlertDescription>
						Nobody outside the team can see this incident.
						{canManage ? " Edit it to choose the pages that show it." : ""}
					</AlertDescription>
				</Alert>
			) : null}

			<div className="grid grid-cols-1 gap-6 lg:grid-cols-[minmax(0,1fr)_320px]">
				<div className="flex min-w-0 flex-col gap-6">
					{canManage ? (
						<Block
							title="Post an update"
							aside="Public: it appears on the pages as soon as you post it."
						>
							<PostUpdate incident={data} />
						</Block>
					) : (
						<Note>
							Only team owners and admins post updates. You can read the
							timeline below.
						</Note>
					)}
					<Block
						title="Timeline"
						aside={count((data.updates ?? []).length, "update")}
					>
						<Timeline incident={data} />
					</Block>
				</div>

				<aside className="flex min-w-0 flex-col gap-6">
					<Block title="Posted to">
						{posted.length === 0 ? (
							<Note>No pages.</Note>
						) : (
							<ul className="m-0 flex list-none flex-col gap-1.5 p-0 text-sm">
								{posted.map((id) => {
									const page = pageById.get(id);
									return (
										<li key={id} className="flex flex-col">
											{page ? (
												<Link
													to="/status-pages/$pageId"
													params={{ pageId: id }}
													className="w-fit rounded-sm text-ink-1 underline-offset-2 outline-none hover:underline focus-visible:focus-ring"
												>
													{page.title}
												</Link>
											) : (
												<span className="text-ink-3">
													A page you cannot see
												</span>
											)}
											{page?.hostname ? (
												<span className="font-mono text-2xs text-ink-3">
													{page.hostname}
													{page.published ? "" : " · draft"}
												</span>
											) : null}
										</li>
									);
								})}
							</ul>
						)}
					</Block>
					<Block title="Affected services">
						{affected.length === 0 ? (
							<Note>None marked.</Note>
						) : (
							<ul className="m-0 flex list-none flex-col gap-1.5 p-0 text-sm">
								{affected.map((id) => {
									const service = serviceNames.get(id);
									return (
										<li key={id} className="flex flex-col">
											<span className="text-ink-1">
												{service?.name ?? "A service you cannot see"}
											</span>
											{service ? (
												<span className="text-2xs text-ink-3">
													{service.projectName} · {service.environmentName}
												</span>
											) : null}
										</li>
									);
								})}
							</ul>
						)}
					</Block>
					<Note>
						Incidents are public statements. What you write here is shown as
						written, with no sign-in, on every page above. Your dashboard stays
						private.
					</Note>
				</aside>
			</div>

			<IncidentDialog
				open={editing}
				onOpenChange={setEditing}
				incident={data}
			/>
		</PageFrame>
	);
}
