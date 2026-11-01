import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useRouter } from "@tanstack/react-router";
import { type FormEvent, useEffect, useState } from "react";
import { errorCode, errorMessage } from "#/api/client-config";
import { createTeam, switchTeam } from "#/api/gen/sdk.gen";
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
import { toast } from "#/components/ui/toast";

/**
 * Creates a team and moves into it: a team has its own projects, servers,
 * tokens and settings, so the new, empty team is the next thing to set up.
 */
export function CreateTeamDialog({
	open,
	onOpenChange,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
}) {
	const queryClient = useQueryClient();
	const router = useRouter();
	const [name, setName] = useState("");

	useEffect(() => {
		if (open) setName("");
	}, [open]);

	const create = useMutation({
		mutationFn: async () => {
			const { data: team } = await createTeam({ body: { name: name.trim() } });
			await switchTeam({ path: { teamId: team.id } });
			return team;
		},
		onSuccess: async (team) => {
			onOpenChange(false);
			// Every cached response belongs to the team just left.
			queryClient.clear();
			await router.invalidate();
			await router.navigate({ to: "/" });
			toast.success(`Created ${team.name}`, "You are its owner.");
		},
	});

	const valid = name.trim() !== "";
	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (!valid || create.isPending) return;
		create.mutate();
	};
	const disabled =
		create.error && errorCode(create.error) === "team_creation_disabled";

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="md">
				<form onSubmit={submit} className="contents">
					<DialogHeader>
						<DialogTitle>Create a team</DialogTitle>
						<DialogDescription>
							A team keeps its own projects, servers, integrations, API tokens
							and audit log. You become its owner and can invite people.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						<Field>
							<FieldLabel htmlFor="team-name">Team name</FieldLabel>
							<Input
								id="team-name"
								scale="lg"
								autoFocus
								autoComplete="off"
								maxLength={80}
								value={name}
								onChange={(event) => setName(event.target.value)}
							/>
							<FieldDescription>
								Servers are never shared between teams.
							</FieldDescription>
						</Field>
						{create.isError ? (
							<FieldError>
								{disabled
									? "The instance administrator has turned team creation off."
									: errorMessage(create.error)}
							</FieldError>
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
						<Button
							type="submit"
							variant={valid ? "primary" : "secondary"}
							disabled={!valid || create.isPending}
						>
							{create.isPending ? <Spinner /> : null}
							Create team
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
