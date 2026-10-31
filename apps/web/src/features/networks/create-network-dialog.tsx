import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { createNetwork } from "#/api/gen/sdk.gen";
import { serversQuery } from "#/api/hooks";
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
import { invalidateNetworks } from "./queries";

/** Name and server of a new shared network. */
export function CreateNetworkDialog({
	open,
	onOpenChange,
}: {
	open: boolean;
	onOpenChange(open: boolean): void;
}) {
	const queryClient = useQueryClient();
	const servers = useQuery({ ...serversQuery(), enabled: open });
	const usable = (servers.data ?? []).filter(
		(server) => server.status !== "revoked" && server.status !== "pending",
	);
	const [name, setName] = useState("");
	const [chosen, setChosen] = useState("");
	const serverId = chosen || usable[0]?.id || "";

	const create = useMutation({
		mutationFn: async () => {
			const { data } = await createNetwork({
				body: { name: name.trim(), serverId },
			});
			return data;
		},
		onSuccess: async (network) => {
			await invalidateNetworks(queryClient);
			toast.success(
				`Created ${network?.name ?? name.trim()}`,
				"Attach services to it from their settings.",
			);
			setName("");
			setChosen("");
			onOpenChange(false);
		},
	});

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (name.trim() !== "" && serverId !== "") create.mutate();
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="md">
				<form onSubmit={submit} className="contents">
					<DialogHeader>
						<DialogTitle>New shared network</DialogTitle>
						<DialogDescription>
							A private network on one server. Services of any project of this
							team that run there can join it and reach each other by hostname.
							Nothing outside the team is ever on it.
						</DialogDescription>
					</DialogHeader>
					<DialogBody className="flex flex-col gap-4">
						<Field>
							<FieldLabel htmlFor="network-name">Name</FieldLabel>
							<Input
								id="network-name"
								value={name}
								maxLength={63}
								autoFocus
								placeholder="backend"
								onChange={(event) => setName(event.target.value)}
							/>
							<FieldDescription>
								Letters, digits, spaces, dots, dashes and underscores.
							</FieldDescription>
						</Field>
						<Field>
							<FieldLabel htmlFor="network-server">Server</FieldLabel>
							<Select
								value={serverId}
								items={usable.map((s) => ({ value: s.id, label: s.name }))}
								onValueChange={(value) => setChosen(String(value ?? ""))}
							>
								<SelectTrigger id="network-server" className="w-full">
									<SelectValue
										placeholder={
											servers.isPending ? "Loading servers…" : "No server yet"
										}
									/>
								</SelectTrigger>
								<SelectContent alignItemWithTrigger={false}>
									{usable.map((s) => (
										<SelectItem key={s.id} value={s.id}>
											{s.name}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
							<FieldDescription>
								A network never spans servers: only services placed on this one
								can join.
							</FieldDescription>
						</Field>
						{create.isError ? (
							<FieldError>{errorMessage(create.error)}</FieldError>
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
							variant="primary"
							disabled={
								name.trim() === "" || serverId === "" || create.isPending
							}
						>
							{create.isPending ? <Spinner /> : null}
							Create network
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
