import { useMutation, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { createSecretManager, updateSecretManager } from "#/api/gen/sdk.gen";
import type { SecretManager } from "#/api/gen/types.gen";
import { CredentialHelp } from "#/components/common/credential-help";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
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
import {
	dopplerGuide,
	infisicalGuide,
	referenceExample,
	referenceHelp,
	type SecretManagerKind,
	type VaultAuth,
	vaultGuide,
} from "#/lib/secret-manager-guides";
import {
	type Draft,
	draftFor,
	isInsecureAddress,
	needsSecret,
	toBody,
	validateDraft,
} from "./model";
import { invalidateSecretManagers } from "./queries";
import { ScopeField } from "./scope-field";

/** Add or edit a secret manager. The credential is write-only: an edit that keeps the address and identity leaves it empty. */
export function SecretManagerDialog({
	open,
	onOpenChange,
	manager,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	manager: SecretManager | null;
}) {
	const queryClient = useQueryClient();
	const editing = manager !== null;
	const [draft, setDraft] = useState<Draft>(() => draftFor(manager));
	const [error, setError] = useState<string | null>(null);
	const [touched, setTouched] = useState(false);

	useEffect(() => {
		if (open) {
			setDraft(draftFor(manager));
			setError(null);
			setTouched(false);
		}
	}, [open, manager]);

	const set = <K extends keyof Draft>(key: K, value: Draft[K]) =>
		setDraft((prev) => ({ ...prev, [key]: value }));

	const errors = validateDraft(draft, manager);
	const valid = Object.keys(errors).length === 0;
	const show = (field: keyof Draft) => (touched ? errors[field] : undefined);

	const save = useMutation({
		mutationFn: async () => {
			const body = toBody(draft);
			if (manager) {
				await updateSecretManager({ path: { id: manager.id }, body });
				return;
			}
			await createSecretManager({
				body: { ...body, name: draft.name.trim(), kind: draft.kind },
			});
		},
		onSuccess: async () => {
			toast.success(
				editing ? "Secret manager saved" : "Secret manager added",
				"Test it to confirm NebulaCtrl can sign in and read a secret.",
			);
			await invalidateSecretManagers(queryClient);
			onOpenChange(false);
		},
		onError: (e) => setError(errorMessage(e)),
	});

	const submit = (event: FormEvent) => {
		event.preventDefault();
		setTouched(true);
		if (!valid || save.isPending) return;
		setError(null);
		save.mutate();
	};

	const vault = draft.kind === "vault";
	const doppler = draft.kind === "doppler";
	const secretLabel = vault
		? draft.authMethod === "approle"
			? "Secret id"
			: "Token"
		: doppler
			? "Token"
			: "Client secret";
	const keepSecret = editing && !needsSecret(draft, manager);

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="lg">
				<form onSubmit={submit} className="contents" noValidate>
					<DialogHeader>
						<DialogTitle>
							{editing ? `Edit ${manager.name}` : "Add a secret manager"}
						</DialogTitle>
						<DialogDescription>
							Variables reference its secrets as{" "}
							<code className="type-mono">
								{referenceExample(
									draft.kind,
									draft.name,
									draft.defaultProject.trim() !== "",
								)}
							</code>
							. NebulaCtrl reads the value when a deploy starts, sends it to the
							server and never stores it.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						{editing ? null : (
							<Field>
								<FieldLabel id="sm-kind-label">Type</FieldLabel>
								<Segmented<SecretManagerKind>
									aria-label="Secret manager type"
									font="sans"
									density="regular"
									value={draft.kind}
									onValueChange={(v) => set("kind", v)}
									options={[
										{ value: "vault", label: "Vault / OpenBao" },
										{ value: "infisical", label: "Infisical" },
										{ value: "doppler", label: "Doppler" },
									]}
								/>
							</Field>
						)}
						<Field>
							<FieldLabel htmlFor="sm-name">Name</FieldLabel>
							<Input
								id="sm-name"
								autoFocus={!editing}
								autoComplete="off"
								spellCheck={false}
								placeholder={vault ? "bao" : doppler ? "doppler" : "infisical"}
								value={draft.name}
								disabled={editing}
								onChange={(e) => set("name", e.target.value)}
							/>
							{show("name") ? (
								<FieldError>{show("name")}</FieldError>
							) : (
								<FieldDescription>
									{editing
										? "The name is part of every reference, so it cannot change."
										: referenceHelp(draft.kind)}
								</FieldDescription>
							)}
						</Field>
						{vault ? (
							<>
								<Field>
									<FieldLabel htmlFor="sm-address">Address</FieldLabel>
									<Input
										id="sm-address"
										autoComplete="off"
										spellCheck={false}
										placeholder="https://vault.example.com:8200"
										value={draft.address}
										onChange={(e) => set("address", e.target.value)}
									/>
									{show("address") ? (
										<FieldError>{show("address")}</FieldError>
									) : null}
								</Field>
								{isInsecureAddress(draft.address) ? (
									<Alert variant="warning">
										<AlertTitle>Plain HTTP</AlertTitle>
										<AlertDescription>
											The credential and every secret cross the network
											unencrypted. Use https unless this is a private network
											you trust.
										</AlertDescription>
									</Alert>
								) : null}
								<div className="grid gap-3 sm:grid-cols-2">
									<Field>
										<FieldLabel htmlFor="sm-mount">KV v2 mount</FieldLabel>
										<Input
											id="sm-mount"
											autoComplete="off"
											spellCheck={false}
											placeholder="secret"
											value={draft.mount}
											onChange={(e) => set("mount", e.target.value)}
										/>
										{show("mount") ? (
											<FieldError>{show("mount")}</FieldError>
										) : null}
									</Field>
									<Field>
										<FieldLabel htmlFor="sm-namespace">
											Namespace (optional)
										</FieldLabel>
										<Input
											id="sm-namespace"
											autoComplete="off"
											spellCheck={false}
											placeholder="team-a"
											value={draft.namespace}
											onChange={(e) => set("namespace", e.target.value)}
										/>
									</Field>
								</div>
								<Field>
									<FieldLabel id="sm-auth-label">Sign in with</FieldLabel>
									<Segmented<VaultAuth>
										aria-label="Vault authentication"
										font="sans"
										density="regular"
										value={draft.authMethod}
										onValueChange={(v) => set("authMethod", v)}
										options={[
											{ value: "token", label: "Token" },
											{ value: "approle", label: "AppRole" },
										]}
									/>
								</Field>
								{draft.authMethod === "approle" ? (
									<div className="grid gap-3 sm:grid-cols-2">
										<Field>
											<FieldLabel htmlFor="sm-role-id">Role id</FieldLabel>
											<Input
												id="sm-role-id"
												autoComplete="off"
												spellCheck={false}
												className="font-mono"
												value={draft.roleId}
												onChange={(e) => set("roleId", e.target.value)}
											/>
											{show("roleId") ? (
												<FieldError>{show("roleId")}</FieldError>
											) : null}
										</Field>
										<Field>
											<FieldLabel htmlFor="sm-approle-mount">
												AppRole mount
											</FieldLabel>
											<Input
												id="sm-approle-mount"
												autoComplete="off"
												spellCheck={false}
												placeholder="approle"
												value={draft.approleMount}
												onChange={(e) => set("approleMount", e.target.value)}
											/>
											{show("approleMount") ? (
												<FieldError>{show("approleMount")}</FieldError>
											) : null}
										</Field>
									</div>
								) : null}
								<Field>
									<FieldLabel htmlFor="sm-ca">
										CA certificate (optional)
									</FieldLabel>
									<Textarea
										id="sm-ca"
										rows={3}
										spellCheck={false}
										className="font-mono text-xs"
										placeholder="-----BEGIN CERTIFICATE-----"
										value={draft.caCert}
										onChange={(e) => set("caCert", e.target.value)}
									/>
									{show("caCert") ? (
										<FieldError>{show("caCert")}</FieldError>
									) : (
										<FieldDescription>
											Only for a server whose certificate comes from your own
											authority. Public material.
										</FieldDescription>
									)}
								</Field>
							</>
						) : doppler ? (
							<>
								<Field>
									<FieldLabel htmlFor="sm-site">
										Doppler API URL (optional)
									</FieldLabel>
									<Input
										id="sm-site"
										autoComplete="off"
										spellCheck={false}
										placeholder="https://api.doppler.com"
										value={draft.siteUrl}
										onChange={(e) => set("siteUrl", e.target.value)}
									/>
									{show("siteUrl") ? (
										<FieldError>{show("siteUrl")}</FieldError>
									) : (
										<FieldDescription>
											Empty is Doppler Cloud. Set it only for a private Doppler
											deployment.
										</FieldDescription>
									)}
								</Field>
								{isInsecureAddress(draft.siteUrl) ? (
									<Alert variant="warning">
										<AlertTitle>Plain HTTP</AlertTitle>
										<AlertDescription>
											The token and every secret cross the network unencrypted.
											Use https unless this is a private network you trust.
										</AlertDescription>
									</Alert>
								) : null}
								<div className="grid gap-3 sm:grid-cols-2">
									<Field>
										<FieldLabel htmlFor="sm-default-project">
											Default project (optional)
										</FieldLabel>
										<Input
											id="sm-default-project"
											autoComplete="off"
											spellCheck={false}
											className="font-mono"
											placeholder="backend"
											value={draft.defaultProject}
											onChange={(e) => set("defaultProject", e.target.value)}
										/>
										{show("defaultProject") ? (
											<FieldError>{show("defaultProject")}</FieldError>
										) : null}
									</Field>
									<Field>
										<FieldLabel htmlFor="sm-default-config">
											Default config (optional)
										</FieldLabel>
										<Input
											id="sm-default-config"
											autoComplete="off"
											spellCheck={false}
											className="font-mono"
											placeholder="prd"
											value={draft.defaultConfig}
											onChange={(e) => set("defaultConfig", e.target.value)}
										/>
										{show("defaultConfig") ? (
											<FieldError>{show("defaultConfig")}</FieldError>
										) : null}
									</Field>
								</div>
								<FieldDescription>
									With a default, a reference can leave the project and config
									out: doppler:{draft.name.trim() || "name"}#KEY. A service
									token is bound to one config and needs no default; a service
									account or personal token needs one, or the project and config
									in each reference.
								</FieldDescription>
							</>
						) : (
							<>
								<Field>
									<FieldLabel htmlFor="sm-site">
										Infisical URL (optional)
									</FieldLabel>
									<Input
										id="sm-site"
										autoComplete="off"
										spellCheck={false}
										placeholder="https://us.infisical.com"
										value={draft.siteUrl}
										onChange={(e) => set("siteUrl", e.target.value)}
									/>
									{show("siteUrl") ? (
										<FieldError>{show("siteUrl")}</FieldError>
									) : (
										<FieldDescription>
											Empty is Infisical Cloud (US). Use
											https://eu.infisical.com or your own instance otherwise.
										</FieldDescription>
									)}
								</Field>
								<div className="grid gap-3 sm:grid-cols-2">
									<Field>
										<FieldLabel htmlFor="sm-client-id">Client id</FieldLabel>
										<Input
											id="sm-client-id"
											autoComplete="off"
											spellCheck={false}
											className="font-mono"
											value={draft.clientId}
											onChange={(e) => set("clientId", e.target.value)}
										/>
										{show("clientId") ? (
											<FieldError>{show("clientId")}</FieldError>
										) : null}
									</Field>
									<Field>
										<FieldLabel htmlFor="sm-project">Project id</FieldLabel>
										<Input
											id="sm-project"
											autoComplete="off"
											spellCheck={false}
											className="font-mono"
											value={draft.projectId}
											onChange={(e) => set("projectId", e.target.value)}
										/>
										{show("projectId") ? (
											<FieldError>{show("projectId")}</FieldError>
										) : null}
									</Field>
								</div>
								<Field>
									<FieldLabel htmlFor="sm-org">
										Organization slug (optional)
									</FieldLabel>
									<Input
										id="sm-org"
										autoComplete="off"
										spellCheck={false}
										value={draft.organizationSlug}
										onChange={(e) => set("organizationSlug", e.target.value)}
									/>
									<FieldDescription>
										Only when the identity belongs to more than one
										organization.
									</FieldDescription>
								</Field>
							</>
						)}
						<CredentialHelp
							guide={
								vault
									? vaultGuide({ auth: draft.authMethod, mount: draft.mount })
									: doppler
										? dopplerGuide(draft.siteUrl)
										: infisicalGuide(draft.siteUrl)
							}
							fieldIds={
								vault
									? draft.authMethod === "approle"
										? ["sm-role-id", "sm-secret"]
										: ["sm-secret"]
									: doppler
										? ["sm-default-project", "sm-default-config", "sm-secret"]
										: ["sm-client-id", "sm-project", "sm-secret"]
							}
						/>
						<Field>
							<FieldLabel htmlFor="sm-secret">
								{secretLabel}
								{keepSecret ? " (leave empty to keep)" : ""}
							</FieldLabel>
							<Input
								id="sm-secret"
								type="password"
								autoComplete="new-password"
								spellCheck={false}
								className="font-mono"
								placeholder={keepSecret && manager?.secretSet ? "••••••••" : ""}
								value={draft.secret}
								onChange={(e) => set("secret", e.target.value)}
							/>
							{show("secret") ? (
								<FieldError>{show("secret")}</FieldError>
							) : (
								<FieldDescription>
									Stored encrypted and never shown again.
								</FieldDescription>
							)}
						</Field>
						<ScopeField
							scope={draft.scope}
							grants={draft.grants}
							error={show("grants")}
							onChange={(next) =>
								setDraft((prev) => ({
									...prev,
									scope: next.scope,
									grants: next.grants,
								}))
							}
						/>
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
							{editing ? "Save" : "Add secret manager"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
