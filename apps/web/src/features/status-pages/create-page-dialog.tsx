import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { type FormEvent, useEffect, useState } from "react";
import { errorCode, errorMessage } from "#/api/client-config";
import { createStatusPage } from "#/api/gen/sdk.gen";
import { projectsQuery } from "#/api/hooks";
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
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import {
	EMPTY_PAGE_FORM,
	MAX_PAGE_TITLE,
	MAX_SLUG,
	type PageForm,
	type PageFormErrors,
	pageCreateBody,
	pageProblem,
	slugify,
	validatePageForm,
} from "./model";
import { invalidateStatusPages } from "./queries";

/** The select's value for "no project": a real value, because an empty one reads as "nothing picked". */
const NO_PROJECT = "__none__";

/**
 * Start a status page. It is created as a draft: add its components and
 * hostname in the editor, then publish. Nothing is public until then.
 */
export function CreatePageDialog({
	open,
	onOpenChange,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
}) {
	const queryClient = useQueryClient();
	const navigate = useNavigate();
	const [form, setForm] = useState<PageForm>(EMPTY_PAGE_FORM);
	const [fromProjectId, setFromProjectId] = useState("");
	const projects = useQuery({ ...projectsQuery(), enabled: open });
	const projectList = (projects.data ?? []).filter(
		(project) => !project.system,
	);
	const [touched, setTouched] = useState(false);
	const [server, setServer] = useState<PageFormErrors>({});
	const [error, setError] = useState<string | null>(null);

	useEffect(() => {
		if (!open) return;
		setForm(EMPTY_PAGE_FORM);
		setFromProjectId("");
		setTouched(false);
		setServer({});
		setError(null);
	}, [open]);

	const set = (change: Partial<PageForm>) => {
		setForm((prev) => ({ ...prev, ...change }));
		setServer({});
	};

	const create = useMutation({
		mutationFn: async () => {
			const { data } = await createStatusPage({
				body: pageCreateBody(form, fromProjectId),
				throwOnError: true,
			});
			return data;
		},
		onSuccess: async (page) => {
			await invalidateStatusPages(queryClient);
			onOpenChange(false);
			toast.success(
				"Status page created",
				"It is a draft. Add components and a hostname, then publish.",
			);
			void navigate({
				to: "/status-pages/$pageId",
				params: { pageId: page.id },
			});
		},
		onError: (e) => {
			const problem = pageProblem(errorCode(e));
			if (
				problem &&
				(problem.field === "slug" ||
					problem.field === "hostname" ||
					problem.field === "title")
			) {
				setServer({ [problem.field]: problem.message });
			} else {
				setError(problem?.message ?? errorMessage(e));
			}
		},
	});

	const errors: PageFormErrors = {
		...validatePageForm(form, "create"),
		...server,
	};
	const blocked = Object.keys(errors).length > 0;

	const submit = (event: FormEvent) => {
		event.preventDefault();
		setTouched(true);
		if (blocked || create.isPending) return;
		setError(null);
		create.mutate();
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="md">
				<form onSubmit={submit} className="contents">
					<DialogHeader>
						<DialogTitle>New status page</DialogTitle>
						<DialogDescription>
							A public page that shows how your services are doing, on your own
							domain. Only the page is public; your dashboard stays private.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						<Field>
							<FieldLabel htmlFor="new-page-title">Title</FieldLabel>
							<Input
								id="new-page-title"
								autoFocus
								autoComplete="off"
								maxLength={MAX_PAGE_TITLE + 20}
								placeholder="Acme status"
								aria-invalid={
									(touched || server.title !== undefined) &&
									errors.title !== undefined
								}
								value={form.title}
								onChange={(e) => set({ title: e.target.value })}
							/>
							{touched || server.title ? (
								<FieldError>{errors.title}</FieldError>
							) : null}
						</Field>
						<Field>
							<FieldLabel htmlFor="new-page-slug">
								Short name (optional)
							</FieldLabel>
							<Input
								id="new-page-slug"
								autoComplete="off"
								spellCheck={false}
								maxLength={MAX_SLUG + 10}
								className="font-mono"
								placeholder={slugify(form.title)}
								aria-invalid={errors.slug !== undefined}
								value={form.slug}
								onChange={(e) => set({ slug: e.target.value })}
							/>
							<FieldDescription>
								Names the page inside your team, for the API and scripts, like
								acme-status. Left empty, it follows the title.
							</FieldDescription>
							<FieldError>{errors.slug}</FieldError>
						</Field>
						<Field>
							<FieldLabel htmlFor="new-page-hostname">
								Hostname (optional)
							</FieldLabel>
							<Input
								id="new-page-hostname"
								autoComplete="off"
								spellCheck={false}
								className="font-mono"
								placeholder="status.example.com"
								aria-invalid={errors.hostname !== undefined}
								value={form.hostname}
								onChange={(e) => set({ hostname: e.target.value })}
							/>
							<FieldDescription>
								A name on a domain you own. You can add it later; the editor
								shows the DNS records to create.
							</FieldDescription>
							<FieldError>{errors.hostname}</FieldError>
						</Field>
						<Field>
							<FieldLabel htmlFor="new-page-project">
								Start from project (optional)
							</FieldLabel>
							<Select
								value={fromProjectId === "" ? NO_PROJECT : fromProjectId}
								items={[
									{ value: NO_PROJECT, label: "None" },
									...projectList.map((project) => ({
										value: project.id,
										label: project.name,
									})),
								]}
								onValueChange={(next) => {
									const picked = String(next ?? NO_PROJECT);
									setFromProjectId(picked === NO_PROJECT ? "" : picked);
									setError(null);
								}}
							>
								<SelectTrigger id="new-page-project" className="w-full">
									<SelectValue />
								</SelectTrigger>
								<SelectContent alignItemWithTrigger={false}>
									<SelectItem value={NO_PROJECT}>None</SelectItem>
									{projectList.map((project) => (
										<SelectItem key={project.id} value={project.id}>
											{project.name}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
							<FieldDescription>
								Adds the project's production services that have a public
								domain. You can edit them afterwards.
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
							disabled={create.isPending}
						>
							{create.isPending ? <Spinner /> : null}
							Create page
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
