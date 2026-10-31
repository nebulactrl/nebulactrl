import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { type FormEvent, useEffect, useRef, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { createProject } from "#/api/gen/sdk.gen";
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
import { Field, FieldError, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Spinner } from "#/components/ui/spinner";
import { Textarea } from "#/components/ui/textarea";
import { toast } from "#/components/ui/toast";

/**
 * Creates a project (which comes with a `production` environment) and opens
 * it. One Idempotency-Key per opening, so a double submit never creates two.
 */
export function NewProjectDialog({
	open,
	onOpenChange,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
}) {
	const queryClient = useQueryClient();
	const navigate = useNavigate();
	const [name, setName] = useState("");
	const [description, setDescription] = useState("");
	const idempotencyKey = useRef(crypto.randomUUID());

	useEffect(() => {
		if (!open) return;
		idempotencyKey.current = crypto.randomUUID();
		setName("");
		setDescription("");
	}, [open]);

	const create = useMutation({
		mutationFn: async () => {
			const { data } = await createProject({
				body: {
					name: name.trim(),
					...(description.trim() ? { description: description.trim() } : {}),
				},
				headers: { "Idempotency-Key": idempotencyKey.current },
			});
			return data;
		},
		onSuccess: async (project) => {
			await queryClient.invalidateQueries({
				queryKey: projectsQuery().queryKey,
			});
			onOpenChange(false);
			toast.success(
				`Created ${project.name}`,
				"Opening its production canvas.",
			);
			void navigate({
				to: "/p/$projectId/$envSlug",
				params: { projectId: project.id, envSlug: "production" },
			});
		},
	});

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (name.trim() === "" || create.isPending) return;
		create.mutate();
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent>
				<form onSubmit={submit} className="contents">
					<DialogHeader>
						<DialogTitle>New project</DialogTitle>
						<DialogDescription>
							A project groups services into environments. It starts with a
							production environment.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						<Field>
							<FieldLabel htmlFor="project-name">Name</FieldLabel>
							<Input
								id="project-name"
								scale="lg"
								autoFocus
								autoComplete="off"
								placeholder="storefront"
								value={name}
								onChange={(event) => setName(event.target.value)}
								aria-invalid={create.isError}
							/>
						</Field>
						<Field>
							<FieldLabel htmlFor="project-description">
								Description (optional)
							</FieldLabel>
							<Textarea
								id="project-description"
								value={description}
								onChange={(event) => setDescription(event.target.value)}
							/>
						</Field>
						{create.isError ? (
							<FieldError>{errorMessage(create.error)}</FieldError>
						) : null}
					</DialogBody>
					<DialogFooter>
						<Button
							type="button"
							variant="secondary"
							onClick={() => onOpenChange(false)}
						>
							Cancel
						</Button>
						<Button
							type="submit"
							variant="primary"
							disabled={name.trim() === "" || create.isPending}
						>
							{create.isPending ? <Spinner /> : null}
							Create project
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
