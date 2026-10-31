import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { type FormEvent, useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { createStatusIncident, updateStatusIncident } from "#/api/gen/sdk.gen";
import type { StatusIncidentView } from "#/api/gen/types.gen";
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
import { Segmented } from "#/components/ui/segmented";
import { Spinner } from "#/components/ui/spinner";
import { Textarea } from "#/components/ui/textarea";
import { toast } from "#/components/ui/toast";
import { useTeamServices } from "#/features/log-drains/use-team-services";
import {
	emptyIncidentForm,
	hasErrors,
	IMPACT_HINT,
	IMPACT_LABEL,
	INCIDENT_IMPACTS,
	INCIDENT_STATUSES,
	type IncidentForm,
	type IncidentImpact,
	type IncidentStatus,
	incidentCreateBody,
	incidentForm,
	incidentPatch,
	MAX_INCIDENT_TITLE,
	MAX_UPDATE_BODY,
	STATUS_HINT,
	STATUS_LABEL,
	toggleId,
	validateIncidentForm,
} from "./model";
import { invalidateStatusPages, statusPagesQuery } from "./queries";
import { PageChecklist, ServiceChecklist } from "./service-checklist";

/**
 * Open an incident, or edit one's title, impact, pages and services. An
 * incident is a public statement: everyone who opens a page it is posted to
 * reads it as written, so the dialog says so before anything is typed. The
 * status and the timeline change with updates, on the incident's own page.
 */
export function IncidentDialog({
	open,
	onOpenChange,
	incident,
	defaultPageIds,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	/** The incident to edit; null opens a new one. */
	incident: StatusIncidentView | null;
	/** Pages ticked for a new incident. */
	defaultPageIds?: string[];
}) {
	const queryClient = useQueryClient();
	const navigate = useNavigate();
	const editing = incident !== null;
	const pages = useQuery({ ...statusPagesQuery(), enabled: open });
	const team = useTeamServices(open);

	const [form, setForm] = useState<IncidentForm>(() =>
		incident ? incidentForm(incident) : emptyIncidentForm(defaultPageIds),
	);
	const [error, setError] = useState<string | null>(null);
	const [touched, setTouched] = useState(false);

	// Starts over each time the dialog opens (or is pointed at another
	// incident), not on every refresh of the incident behind it.
	const incidentId = incident?.id;
	const defaultKey = (defaultPageIds ?? []).join(",");
	// biome-ignore lint/correctness/useExhaustiveDependencies: see above
	useEffect(() => {
		if (!open) return;
		setForm(
			incident
				? incidentForm(incident)
				: emptyIncidentForm(defaultKey === "" ? [] : defaultKey.split(",")),
		);
		setError(null);
		setTouched(false);
	}, [open, incidentId, defaultKey]);

	const set = (change: Partial<IncidentForm>) =>
		setForm((prev) => ({ ...prev, ...change }));

	const save = useMutation({
		mutationFn: async () => {
			if (incident) {
				await updateStatusIncident({
					path: { incidentId: incident.id },
					body: incidentPatch(form, incident),
					throwOnError: true,
				});
				return null;
			}
			const { data } = await createStatusIncident({
				body: incidentCreateBody(form),
				throwOnError: true,
			});
			return data;
		},
		onSuccess: async (created) => {
			await invalidateStatusPages(queryClient);
			onOpenChange(false);
			if (created) {
				toast.success(
					"Incident opened",
					form.pageIds.length > 0
						? "It is on the pages you picked now. Post updates as things change."
						: "It is not posted to any page yet. Edit it to choose pages.",
				);
				void navigate({
					to: "/status-pages/incidents/$incidentId",
					params: { incidentId: created.id },
				});
			} else {
				toast.success("Incident saved", "The pages show the change now.");
			}
		},
		onError: (e) => setError(errorMessage(e)),
	});

	const errors = validateIncidentForm(form, editing ? "edit" : "create");
	const changed =
		!incident || Object.keys(incidentPatch(form, incident)).length > 0;
	const blocked = hasErrors(errors) || !changed;

	const submit = (event: FormEvent) => {
		event.preventDefault();
		setTouched(true);
		if (blocked || save.isPending) return;
		setError(null);
		save.mutate();
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="lg">
				<form onSubmit={submit} className="contents">
					<DialogHeader>
						<DialogTitle>
							{incident ? "Edit incident" : "Post an incident"}
						</DialogTitle>
						<DialogDescription>
							Public statement: the pages you pick show it as written, to
							anyone.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						<Field>
							<FieldLabel htmlFor="incident-title">Title</FieldLabel>
							<Input
								id="incident-title"
								autoFocus
								autoComplete="off"
								maxLength={MAX_INCIDENT_TITLE + 20}
								placeholder="Elevated API errors"
								aria-invalid={touched && errors.title !== undefined}
								value={form.title}
								onChange={(e) => set({ title: e.target.value })}
							/>
							{touched ? <FieldError>{errors.title}</FieldError> : null}
						</Field>

						<Field>
							<FieldLabel id="incident-impact-label">Impact</FieldLabel>
							<div>
								<Segmented<IncidentImpact>
									aria-label="Impact"
									font="sans"
									density="regular"
									className="max-w-full flex-wrap"
									value={form.impact}
									onValueChange={(impact) => set({ impact })}
									options={INCIDENT_IMPACTS.map((impact) => ({
										value: impact,
										label: IMPACT_LABEL[impact],
									}))}
								/>
							</div>
							<FieldDescription>
								{IMPACT_HINT[form.impact]} While the incident is open.
							</FieldDescription>
						</Field>

						{editing ? null : (
							<>
								<Field>
									<FieldLabel id="incident-status-label">Status</FieldLabel>
									<div>
										<Segmented<IncidentStatus>
											aria-label="Status"
											font="sans"
											className="max-w-full flex-wrap"
											density="regular"
											value={form.status}
											onValueChange={(status) => set({ status })}
											options={INCIDENT_STATUSES.map((status) => ({
												value: status,
												label: STATUS_LABEL[status],
											}))}
										/>
									</div>
									<FieldDescription>
										{STATUS_HINT[form.status]}
									</FieldDescription>
								</Field>
								<Field>
									<FieldLabel htmlFor="incident-body">First update</FieldLabel>
									<Textarea
										id="incident-body"
										rows={4}
										maxLength={MAX_UPDATE_BODY + 100}
										placeholder="We are seeing a higher than usual number of failed requests and are looking into it."
										aria-invalid={touched && errors.body !== undefined}
										value={form.body}
										onChange={(e) => set({ body: e.target.value })}
									/>
									<FieldDescription>
										Plain text; line breaks stay. It is the first entry of the
										timeline.
									</FieldDescription>
									{touched ? <FieldError>{errors.body}</FieldError> : null}
								</Field>
							</>
						)}

						<Field>
							<FieldLabel id="incident-pages-label">
								Post to these pages
							</FieldLabel>
							<PageChecklist
								label="Status pages to post to"
								pages={pages.data ?? []}
								pending={pages.isPending}
								selected={form.pageIds}
								onToggle={(id, on) =>
									set({ pageIds: toggleId(form.pageIds, id, on) })
								}
							/>
							<FieldDescription>
								Leave every box empty to keep it to yourself for now. Removing a
								page takes the incident off it.
							</FieldDescription>
						</Field>

						<Field>
							<FieldLabel id="incident-services-label">
								Affected services (optional)
							</FieldLabel>
							<ServiceChecklist
								label="Affected services"
								services={team.services}
								pending={team.pending}
								selected={form.serviceIds}
								onToggle={(service, on) =>
									set({ serviceIds: toggleId(form.serviceIds, service.id, on) })
								}
							/>
							<FieldDescription>
								Each page marks the affected services it shows, under the public
								names you gave them.
							</FieldDescription>
						</Field>

						{error ? <FieldError>{error}</FieldError> : null}
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
							variant={blocked ? "secondary" : "primary"}
							disabled={save.isPending || (editing && !changed)}
						>
							{save.isPending ? <Spinner /> : null}
							{editing ? "Save incident" : "Post incident"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
