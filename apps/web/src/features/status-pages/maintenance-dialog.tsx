import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import {
	createMaintenanceWindow,
	updateMaintenanceWindow,
} from "#/api/gen/sdk.gen";
import type { MaintenanceWindowView } from "#/api/gen/types.gen";
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
import { Textarea } from "#/components/ui/textarea";
import { toast } from "#/components/ui/toast";
import { useTeamServices } from "#/features/log-drains/use-team-services";
import {
	emptyWindowForm,
	fromLocalInput,
	hasErrors,
	MAX_INCIDENT_TITLE,
	MAX_WINDOW_DESCRIPTION,
	toggleId,
	utcLabelOfLocal,
	validateWindowForm,
	type WindowForm,
	windowCreateBody,
	windowForm,
	windowLength,
	windowPatch,
} from "./model";
import { invalidateStatusPages, statusPagesQuery } from "./queries";
import { PageChecklist, ServiceChecklist } from "./service-checklist";

/** "Europe/Berlin": the zone the pickers are read in. */
const ZONE = Intl.DateTimeFormat().resolvedOptions().timeZone;

function DateTimeField({
	id,
	label,
	value,
	error,
	onChange,
}: {
	id: string;
	label: string;
	value: string;
	error: string | undefined;
	onChange: (value: string) => void;
}) {
	const utc = utcLabelOfLocal(value);
	return (
		<Field>
			<FieldLabel htmlFor={id}>{label}</FieldLabel>
			<Input
				id={id}
				type="datetime-local"
				step={60}
				aria-invalid={error !== undefined}
				aria-describedby={`${id}-utc`}
				value={value}
				onChange={(event) => onChange(event.target.value)}
			/>
			<span id={`${id}-utc`} className="font-mono text-2xs text-ink-3">
				{utc ? utc : "Pick a date and time"}
			</span>
			<FieldError>{error}</FieldError>
		</Field>
	);
}

/**
 * Schedule a maintenance window or change one that has not ended. Times are
 * picked in the browser's own time zone; the UTC reading sits under each
 * picker, because the control plane, the agents and the public pages say UTC.
 */
export function MaintenanceDialog({
	open,
	onOpenChange,
	maintenance: current,
	defaultPageIds,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	/** The window to edit; null schedules a new one. */
	maintenance: MaintenanceWindowView | null;
	/** Pages ticked for a new window. */
	defaultPageIds?: string[];
}) {
	const queryClient = useQueryClient();
	const editing = current !== null;
	const pages = useQuery({ ...statusPagesQuery(), enabled: open });
	const team = useTeamServices(open);

	const [form, setForm] = useState<WindowForm>(() =>
		current ? windowForm(current) : emptyWindowForm(new Date(), defaultPageIds),
	);
	const [error, setError] = useState<string | null>(null);
	const [touched, setTouched] = useState(false);

	const currentId = current?.id;
	const defaultKey = (defaultPageIds ?? []).join(",");
	// biome-ignore lint/correctness/useExhaustiveDependencies: starts over when it opens, not on every refresh of the window behind it
	useEffect(() => {
		if (!open) return;
		setForm(
			current
				? windowForm(current)
				: emptyWindowForm(
						new Date(),
						defaultKey === "" ? [] : defaultKey.split(","),
					),
		);
		setError(null);
		setTouched(false);
	}, [open, currentId, defaultKey]);

	const set = (change: Partial<WindowForm>) =>
		setForm((prev) => ({ ...prev, ...change }));

	const save = useMutation({
		mutationFn: async () => {
			if (current) {
				await updateMaintenanceWindow({
					path: { windowId: current.id },
					body: windowPatch(form, current),
					throwOnError: true,
				});
			} else {
				await createMaintenanceWindow({
					body: windowCreateBody(form),
					throwOnError: true,
				});
			}
		},
		onSuccess: async () => {
			await invalidateStatusPages(queryClient);
			toast.success(
				editing ? "Maintenance saved" : "Maintenance scheduled",
				form.pageIds.length > 0
					? "The pages you picked announce it up to 14 days ahead."
					: "No page announces it: pick pages to tell visitors.",
			);
			onOpenChange(false);
		},
		onError: (e) => setError(errorMessage(e)),
	});

	const errors = validateWindowForm(form, new Date());
	const changed =
		!current || Object.keys(windowPatch(form, current)).length > 0;
	const blocked = hasErrors(errors) || !changed;
	const length = windowLength(
		fromLocalInput(form.startsAt) ?? "",
		fromLocalInput(form.endsAt) ?? "",
	);

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
							{current ? "Edit maintenance" : "Schedule maintenance"}
						</DialogTitle>
						<DialogDescription>
							Public statement: the pages you pick announce it to anyone. Its
							services raise no outage alerts while it runs.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						<Field>
							<FieldLabel htmlFor="window-title">Title</FieldLabel>
							<Input
								id="window-title"
								autoFocus
								autoComplete="off"
								maxLength={MAX_INCIDENT_TITLE + 20}
								placeholder="Database upgrade"
								aria-invalid={touched && errors.title !== undefined}
								value={form.title}
								onChange={(e) => set({ title: e.target.value })}
							/>
							{touched ? <FieldError>{errors.title}</FieldError> : null}
						</Field>

						<Field>
							<FieldLabel htmlFor="window-description">
								Description (optional)
							</FieldLabel>
							<Textarea
								id="window-description"
								rows={3}
								maxLength={MAX_WINDOW_DESCRIPTION + 100}
								placeholder="We are upgrading the database. The API is read-only for about 20 minutes."
								aria-invalid={errors.description !== undefined}
								value={form.description}
								onChange={(e) => set({ description: e.target.value })}
							/>
							<FieldError>{errors.description}</FieldError>
						</Field>

						<div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
							<DateTimeField
								id="window-start"
								label="Starts"
								value={form.startsAt}
								error={touched ? errors.startsAt : undefined}
								onChange={(startsAt) => set({ startsAt })}
							/>
							<DateTimeField
								id="window-end"
								label="Ends"
								value={form.endsAt}
								error={errors.endsAt}
								onChange={(endsAt) => set({ endsAt })}
							/>
						</div>
						<p className="m-0 text-ink-3 text-xs">
							Pickers are in your time zone ({ZONE}).
							{length ? ` The window lasts ${length}.` : ""}
						</p>

						<Field>
							<FieldLabel id="window-pages-label">
								Announce on these pages
							</FieldLabel>
							<PageChecklist
								label="Status pages that announce the window"
								pages={pages.data ?? []}
								pending={pages.isPending}
								selected={form.pageIds}
								onToggle={(id, on) =>
									set({ pageIds: toggleId(form.pageIds, id, on) })
								}
							/>
							<FieldDescription>
								Pages show it from 14 days ahead, and while it runs the page's
								state reads "Under maintenance" unless something is worse.
							</FieldDescription>
						</Field>

						<Field>
							<FieldLabel id="window-services-label">
								Services in maintenance
							</FieldLabel>
							<ServiceChecklist
								label="Services in maintenance"
								services={team.services}
								pending={team.pending}
								selected={form.serviceIds}
								onToggle={(service, on) =>
									set({ serviceIds: toggleId(form.serviceIds, service.id, on) })
								}
							/>
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
							{editing ? "Save maintenance" : "Schedule maintenance"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
