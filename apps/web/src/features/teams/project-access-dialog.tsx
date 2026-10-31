import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { listTeamMembersQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import { updateTeamMember } from "#/api/gen/sdk.gen";
import type { TeamMember } from "#/api/gen/types.gen";
import { projectsQuery } from "#/api/hooks";
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
import { Field, FieldDescription, FieldError } from "#/components/ui/field";
import { Segmented } from "#/components/ui/segmented";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { isRole, ROLE_LABEL } from "./roles";

type Scope = "all" | "some";

/**
 * Which projects a developer or viewer can see. Everything outside the list
 * answers 404, as if it did not exist. Owners and admins always see all.
 */
export function ProjectAccessDialog({
	teamId,
	member,
	onOpenChange,
}: {
	teamId: string;
	member: TeamMember | null;
	onOpenChange: (open: boolean) => void;
}) {
	const queryClient = useQueryClient();
	const open = member !== null;
	const projects = useQuery({ ...projectsQuery(), enabled: open });
	const [scope, setScope] = useState<Scope>("all");
	const [chosen, setChosen] = useState<ReadonlySet<string>>(new Set());

	useEffect(() => {
		if (!member) return;
		setScope(member.projectIds ? "some" : "all");
		setChosen(new Set(member.projectIds ?? []));
	}, [member]);

	const save = useMutation({
		mutationFn: async () => {
			if (!member) return;
			await updateTeamMember({
				path: { teamId, userId: member.userId },
				body:
					scope === "all" ? { allProjects: true } : { projectIds: [...chosen] },
			});
		},
		onSuccess: async () => {
			await queryClient.invalidateQueries({
				queryKey: listTeamMembersQueryKey({ path: { teamId } }),
			});
			toast.success(
				"Project access updated",
				scope === "all"
					? `${member?.name} can see every project.`
					: `${member?.name} can see ${chosen.size} ${chosen.size === 1 ? "project" : "projects"}.`,
			);
			onOpenChange(false);
		},
	});

	const toggle = (id: string, on: boolean) => {
		const next = new Set(chosen);
		if (on) next.add(id);
		else next.delete(id);
		setChosen(next);
	};

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (save.isPending) return;
		save.mutate();
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="sm">
				<form onSubmit={submit} className="contents">
					<DialogHeader>
						<DialogTitle>Project access for {member?.name}</DialogTitle>
						<DialogDescription>
							{member && isRole(member.role)
								? `${ROLE_LABEL[member.role]}s`
								: "Members"}{" "}
							can be limited to some projects. They get a 404 for the rest, not
							an access-denied page.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						<Segmented
							aria-label="Project access"
							font="sans"
							density="regular"
							value={scope}
							onValueChange={setScope}
							options={[
								{ value: "all", label: "All projects" },
								{ value: "some", label: "Selected projects" },
							]}
						/>
						{scope === "some" ? (
							<Field>
								<fieldset
									aria-label="Projects this member can see"
									className="surface-well m-0 flex max-h-56 min-w-0 flex-col gap-2 overflow-y-auto overscroll-contain rounded-card p-3"
								>
									{projects.data && projects.data.length > 0 ? (
										projects.data.map((project) => (
											<CheckLabel
												key={project.id}
												className="gap-2.5 text-sm"
												checked={chosen.has(project.id)}
												onCheckedChange={(on) => toggle(project.id, on)}
											>
												{project.name}
											</CheckLabel>
										))
									) : (
										<span className="text-ink-3 text-xs">
											{projects.isPending
												? "Loading projects…"
												: "This team has no projects yet."}
										</span>
									)}
								</fieldset>
								<FieldDescription>
									{chosen.size === 0
										? "Nothing selected: this member sees no project at all."
										: `${chosen.size} selected.`}
								</FieldDescription>
							</Field>
						) : null}
						{save.isError ? (
							<FieldError>{errorMessage(save.error)}</FieldError>
						) : null}
					</DialogBody>
					<DialogFooter>
						<Button
							type="button"
							variant="ghost"
							onClick={() => onOpenChange(false)}
						>
							Cancel
						</Button>
						<Button type="submit" disabled={save.isPending}>
							{save.isPending ? <Spinner /> : null}
							Save access
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
