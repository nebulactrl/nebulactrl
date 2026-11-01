import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { type FormEvent, useEffect, useRef, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { createEnvironment } from "#/api/gen/sdk.gen";
import { environmentsQuery } from "#/api/hooks";
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
import { Field, FieldError, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";

/**
 * Creates an empty environment in a project and opens its canvas. One
 * Idempotency-Key per opening, so a double submit never creates two.
 */
export function NewEnvironmentDialog({
	projectId,
	open,
	onOpenChange,
}: {
	projectId: string;
	open: boolean;
	onOpenChange: (open: boolean) => void;
}) {
	const queryClient = useQueryClient();
	const navigate = useNavigate();
	const [name, setName] = useState("");
	const [isProduction, setIsProduction] = useState(false);
	const idempotencyKey = useRef(crypto.randomUUID());

	useEffect(() => {
		if (!open) return;
		idempotencyKey.current = crypto.randomUUID();
		setName("");
		setIsProduction(false);
	}, [open]);

	const create = useMutation({
		mutationFn: async () => {
			const { data } = await createEnvironment({
				path: { projectId },
				body: { name: name.trim(), isProduction },
				headers: { "Idempotency-Key": idempotencyKey.current },
			});
			return data;
		},
		onSuccess: async (environment) => {
			await queryClient.invalidateQueries({
				queryKey: environmentsQuery(projectId).queryKey,
			});
			onOpenChange(false);
			toast.success(`Created ${environment.name}`, "Opening its canvas.");
			void navigate({
				to: "/p/$projectId/$envSlug",
				params: { projectId, envSlug: environment.slug },
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
						<DialogTitle>New environment</DialogTitle>
						<DialogDescription>
							An environment is its own set of services, variables and domains,
							on its own private network. It starts empty.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						<Field>
							<FieldLabel htmlFor="environment-name">Name</FieldLabel>
							<Input
								id="environment-name"
								scale="lg"
								autoFocus
								autoComplete="off"
								placeholder="staging"
								value={name}
								onChange={(event) => setName(event.target.value)}
								aria-invalid={create.isError}
							/>
						</Field>
						<CheckLabel
							checked={isProduction}
							onCheckedChange={setIsProduction}
							className="items-start"
							checkboxClassName="mt-0.5"
						>
							<span className="flex flex-col gap-0.5">
								<span className="text-ink-1">Production</span>
								<span className="type-secondary text-ink-3">
									Deploys and destructive actions here ask for confirmation.
								</span>
							</span>
						</CheckLabel>
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
							Create environment
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
