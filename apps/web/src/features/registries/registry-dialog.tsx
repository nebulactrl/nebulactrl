import { useMutation, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useState } from "react";
import { errorCode, errorMessage } from "#/api/client-config";
import { CredentialHelp } from "#/components/common/credential-help";
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
import {
	Field,
	FieldDescription,
	FieldError,
	FieldLabel,
} from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { registryGuide, registryPushGuide } from "#/lib/credential-links";
import { createRegistry, updateRegistry } from "./api";
import {
	createBody,
	type DraftErrors,
	draftOf,
	EMPTY_DRAFT,
	HOST_CHANGE_HINT,
	type Registry,
	type RegistryDraft,
	repositoryRoot,
	requiredPasswords,
	secretRequiredErrors,
	updateBody,
	validateDraft,
} from "./model";
import { invalidateRegistries } from "./queries";

const PUSH_FIELDS = ["reg-push-user", "reg-push-password"] as const;
const PULL_FIELDS = ["reg-pull-user", "reg-pull-password"] as const;

/**
 * Add or edit an external registry: any OCI registry that takes a user name
 * and a token (GHCR, Docker Hub, GitLab, Harbor, a self-hosted one). The push
 * credential is what a build server uploads with; an optional read-only one
 * is what the servers that run the app pull with. Both are write-only: an
 * edit shows nothing stored, and an empty field keeps what is.
 */
export function RegistryDialog({
	open,
	onOpenChange,
	registry,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	registry: Registry | null;
}) {
	const queryClient = useQueryClient();
	const editing = registry !== null;
	const [draft, setDraft] = useState<RegistryDraft>(EMPTY_DRAFT);
	const [touched, setTouched] = useState(false);
	const [error, setError] = useState<string | null>(null);
	// Field problems the server reported, shown until the field is edited.
	const [serverErrors, setServerErrors] = useState<DraftErrors>({});

	useEffect(() => {
		if (!open) return;
		setDraft(registry ? draftOf(registry) : EMPTY_DRAFT);
		setTouched(false);
		setError(null);
		setServerErrors({});
	}, [open, registry]);

	const reenter = requiredPasswords(draft, registry);
	const errors: DraftErrors = validateDraft(draft, {
		editing,
		hadPull: registry?.hasPullAuth ?? false,
		reenter,
	});
	const valid = Object.keys(errors).length === 0;
	// A field shows its problem once the person has left it or tried to save,
	// except the host, whose scheme mistake is worth catching as they type.
	const shown = (key: keyof RegistryDraft) =>
		(touched || (key === "host" && draft.host !== "")
			? errors[key]
			: undefined) ?? serverErrors[key];

	const set = <K extends keyof RegistryDraft>(
		key: K,
		value: RegistryDraft[K],
	) => {
		setDraft((current) => ({ ...current, [key]: value }));
		setServerErrors(({ [key]: _cleared, ...rest }) => rest);
	};

	const save = useMutation({
		mutationFn: async () => {
			if (registry) {
				return updateRegistry(registry.id, updateBody(draft, registry));
			}
			return createRegistry(createBody(draft));
		},
		onSuccess: async () => {
			await invalidateRegistries(queryClient);
			toast.success(
				editing ? "Registry saved" : "Registry added",
				editing
					? "Builds and pulls use the new settings from the next deployment."
					: "Use Test registry on a build server to confirm it accepts a push.",
			);
			onOpenChange(false);
		},
		onError: (e) => {
			// The form's own check normally catches this first; the server's
			// answer is the fallback when the stored host and the form disagree.
			if (errorCode(e) === "secret_required") {
				setServerErrors(secretRequiredErrors(draft, registry));
				return;
			}
			setError(errorMessage(e));
		},
	});

	const submit = (event: FormEvent) => {
		event.preventDefault();
		setTouched(true);
		if (!valid || save.isPending) return;
		setError(null);
		setServerErrors({});
		save.mutate();
	};

	const root = repositoryRoot(
		draft.host || "registry.example.com",
		draft.namespace,
	);

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="lg">
				<form onSubmit={submit} className="contents" noValidate>
					<DialogHeader>
						<DialogTitle>
							{registry ? `Edit ${registry.name}` : "Add a registry"}
						</DialogTitle>
						<DialogDescription>
							Any OCI registry that takes a user name and a token. Build servers
							push images here, and the server that runs the app pulls them by
							digest. Credentials are stored encrypted and never shown again.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						<Field>
							<FieldLabel htmlFor="reg-name">Name</FieldLabel>
							<Input
								id="reg-name"
								autoFocus
								autoComplete="off"
								placeholder="GitHub registry"
								value={draft.name}
								aria-invalid={shown("name") !== undefined}
								onChange={(e) => set("name", e.target.value)}
							/>
							<FieldError>{shown("name")}</FieldError>
						</Field>

						<div className="grid gap-4 sm:grid-cols-2">
							<Field>
								<FieldLabel htmlFor="reg-host">Host</FieldLabel>
								<Input
									id="reg-host"
									autoComplete="off"
									spellCheck={false}
									className="font-mono"
									placeholder="ghcr.io"
									value={draft.host}
									aria-invalid={shown("host") !== undefined}
									onChange={(e) => set("host", e.target.value)}
								/>
								<FieldError>{shown("host")}</FieldError>
								{shown("host") === undefined ? (
									<FieldDescription>
										{reenter.push
											? HOST_CHANGE_HINT
											: "The host, and a port if it has one. No https://."}
									</FieldDescription>
								) : null}
							</Field>
							<Field>
								<FieldLabel htmlFor="reg-namespace">Namespace</FieldLabel>
								<Input
									id="reg-namespace"
									autoComplete="off"
									spellCheck={false}
									className="font-mono"
									placeholder="acme"
									value={draft.namespace}
									aria-invalid={shown("namespace") !== undefined}
									onChange={(e) => set("namespace", e.target.value)}
								/>
								<FieldError>{shown("namespace")}</FieldError>
								{shown("namespace") === undefined ? (
									<FieldDescription>
										Your user or organization, lowercase. Empty puts images at
										the registry's root.
									</FieldDescription>
								) : null}
							</Field>
						</div>
						<p className="m-0 text-ink-3 text-xs">
							Images go to{" "}
							<span className="font-mono text-ink-2">
								{root}/&lt;project&gt;-&lt;service&gt;
							</span>
							.
						</p>

						<CredentialHelp
							guide={registryPushGuide(draft.host)}
							fieldIds={PUSH_FIELDS}
						/>
						<div className="grid gap-4 sm:grid-cols-2">
							<Field>
								<FieldLabel htmlFor="reg-push-user">User name</FieldLabel>
								<Input
									id="reg-push-user"
									autoComplete="off"
									spellCheck={false}
									className="font-mono"
									value={draft.pushUsername}
									aria-invalid={shown("pushUsername") !== undefined}
									onChange={(e) => set("pushUsername", e.target.value)}
								/>
								<FieldError>{shown("pushUsername")}</FieldError>
							</Field>
							<Field>
								<FieldLabel htmlFor="reg-push-password">
									Token or password
									{editing && registry.hasPushAuth && !reenter.push
										? " (empty keeps it)"
										: ""}
								</FieldLabel>
								<Input
									id="reg-push-password"
									type="password"
									autoComplete="new-password"
									spellCheck={false}
									className="font-mono"
									placeholder={
										editing && registry.hasPushAuth && !reenter.push
											? "stored"
											: ""
									}
									value={draft.pushPassword}
									aria-invalid={shown("pushPassword") !== undefined}
									onChange={(e) => set("pushPassword", e.target.value)}
								/>
								<FieldError>{shown("pushPassword")}</FieldError>
								{reenter.push && shown("pushPassword") === undefined ? (
									<FieldDescription>{HOST_CHANGE_HINT}</FieldDescription>
								) : null}
							</Field>
						</div>

						<CheckLabel
							checked={draft.pullEnabled}
							onCheckedChange={(checked) => set("pullEnabled", checked)}
							className="items-start"
							checkboxClassName="mt-0.5"
						>
							<span className="flex flex-col gap-0.5">
								<span className="text-ink-1 text-sm">
									Use a separate read-only credential to pull
								</span>
								<span className="text-ink-3 text-xs">
									Servers that run the app only need to read. Without it they
									pull with the credential above.
								</span>
							</span>
						</CheckLabel>
						{draft.pullEnabled ? (
							<>
								<CredentialHelp
									guide={registryGuide(
										draft.host.trim() === "" ? "" : `${draft.host.trim()}/x`,
									)}
									fieldIds={PULL_FIELDS}
								/>
								<div className="grid gap-4 sm:grid-cols-2">
									<Field>
										<FieldLabel htmlFor="reg-pull-user">
											Pull user name
										</FieldLabel>
										<Input
											id="reg-pull-user"
											autoComplete="off"
											spellCheck={false}
											className="font-mono"
											value={draft.pullUsername}
											aria-invalid={shown("pullUsername") !== undefined}
											onChange={(e) => set("pullUsername", e.target.value)}
										/>
										<FieldError>{shown("pullUsername")}</FieldError>
									</Field>
									<Field>
										<FieldLabel htmlFor="reg-pull-password">
											Pull token or password
											{editing && registry.hasPullAuth && !reenter.pull
												? " (empty keeps it)"
												: ""}
										</FieldLabel>
										<Input
											id="reg-pull-password"
											type="password"
											autoComplete="new-password"
											spellCheck={false}
											className="font-mono"
											placeholder={
												editing && registry.hasPullAuth && !reenter.pull
													? "stored"
													: ""
											}
											value={draft.pullPassword}
											aria-invalid={shown("pullPassword") !== undefined}
											onChange={(e) => set("pullPassword", e.target.value)}
										/>
										<FieldError>{shown("pullPassword")}</FieldError>
										{reenter.pull && shown("pullPassword") === undefined ? (
											<FieldDescription>{HOST_CHANGE_HINT}</FieldDescription>
										) : null}
									</Field>
								</div>
							</>
						) : null}

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
						<Button type="submit" variant="primary" disabled={save.isPending}>
							{save.isPending ? <Spinner /> : null}
							{editing ? "Save" : "Add registry"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
