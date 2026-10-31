import { type FormEvent, useState } from "react";
import { errorMessage } from "#/api/client-config";
import type { Project } from "#/api/models";
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
import { Field, FieldError, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { useUpdateProject } from "./use-update-project";

type Target = Pick<Project, "id" | "name">;

/** The dialog's content mounts on each opening, so it starts from the current name. */
function RenameForm({
	project,
	onDone,
}: {
	project: Target;
	onDone: () => void;
}) {
	const [name, setName] = useState(project.name);
	const save = useUpdateProject(project, (saved) => {
		onDone();
		toast.success(`Renamed to ${saved.name ?? project.name}`);
	});

	const trimmed = name.trim();
	const unchanged = trimmed === project.name;
	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (trimmed === "" || unchanged || save.isPending) return;
		save.mutate({ name: trimmed });
	};

	return (
		<form onSubmit={submit} className="contents">
			<DialogHeader>
				<DialogTitle>Rename project</DialogTitle>
				<DialogDescription>
					Changes the name shown in the dashboard. The slug and the addresses
					built from it stay the same.
				</DialogDescription>
			</DialogHeader>
			<DialogBody>
				<Field>
					<FieldLabel htmlFor="rename-project-name">Name</FieldLabel>
					<Input
						id="rename-project-name"
						scale="lg"
						autoFocus
						autoComplete="off"
						value={name}
						onChange={(event) => setName(event.target.value)}
						aria-invalid={save.isError}
					/>
				</Field>
				{save.isError ? (
					<FieldError>{errorMessage(save.error)}</FieldError>
				) : null}
			</DialogBody>
			<DialogFooter>
				<Button type="button" variant="secondary" onClick={onDone}>
					Cancel
				</Button>
				<Button
					type="submit"
					variant="primary"
					disabled={trimmed === "" || unchanged || save.isPending}
				>
					{save.isPending ? <Spinner /> : null}
					Save name
				</Button>
			</DialogFooter>
		</form>
	);
}

/** Renames a project: the slug and every address built from it stay as they are. */
export function RenameProjectDialog({
	project,
	open,
	onOpenChange,
}: {
	project: Target;
	open: boolean;
	onOpenChange: (open: boolean) => void;
}) {
	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent>
				<RenameForm project={project} onDone={() => onOpenChange(false)} />
			</DialogContent>
		</Dialog>
	);
}
