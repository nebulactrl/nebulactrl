import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { listGitConnectionsOptions } from "#/api/gen/@tanstack/react-query.gen";
import { linkServiceGit, stageServiceSourceChange } from "#/api/gen/sdk.gen";
import type { StageServiceSourceChangeInputBody } from "#/api/gen/types.gen";
import { invalidateServices } from "#/api/invalidate";
import type { Service } from "#/api/models";
import { CredentialHelp } from "#/components/common/credential-help";
import { Button } from "#/components/ui/button";
import {
	Dialog,
	DialogBody,
	DialogContent,
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
	RepoPicker,
	type RepoSelection,
	usableConnections,
} from "#/features/git/repo-picker";
import { gitCloneGuide, registryGuide } from "#/lib/credential-links";
import { BUILD_TYPES } from "./build-types";
import { looksLikeSshUrl } from "./ssh-model";

type SourceKind = "image" | "git";

const BUILD_ITEMS = BUILD_TYPES.map((type) => ({
	value: type.value,
	label: type.text,
}));

/**
 * Switch a service between a Docker image and a Git repository. The change is
 * staged like any setting: the next deploy applies it. Name, domains,
 * variables and volumes are untouched.
 */
export function ChangeSourceDialog({
	service,
	open,
	onOpenChange,
}: {
	service: Service;
	open: boolean;
	onOpenChange(open: boolean): void;
}) {
	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent width={520}>
				<ChangeSourceForm
					service={service}
					onClose={() => onOpenChange(false)}
				/>
			</DialogContent>
		</Dialog>
	);
}

/** Mounts with the dialog, so every opening starts from a clean form. */
function ChangeSourceForm({
	service,
	onClose,
}: {
	service: Service;
	onClose(): void;
}) {
	const queryClient = useQueryClient();
	const current: SourceKind = service.sourceType === "git" ? "git" : "image";
	// The current type is not a choice: switching to it would change nothing.
	const [target, setTarget] = useState<SourceKind>(
		current === "git" ? "image" : "git",
	);
	const [image, setImage] = useState("");
	const [registryUsername, setRegistryUsername] = useState("");
	const [registryPassword, setRegistryPassword] = useState("");
	const [gitUrl, setGitUrl] = useState("");
	const [gitBranch, setGitBranch] = useState("main");
	const [gitToken, setGitToken] = useState("");
	const [buildType, setBuildType] = useState("auto");
	// Pick from a connected git account, or paste any URL.
	const gitConnections = useQuery({
		...listGitConnectionsOptions(),
		enabled: target === "git",
	});
	const hasAccounts =
		usableConnections(gitConnections.data?.items ?? []).length > 0;
	const [gitMode, setGitMode] = useState<"account" | "url">("account");
	const [gitSel, setGitSel] = useState<RepoSelection | null>(null);
	const accountMode = target === "git" && hasAccounts && gitMode === "account";
	const gitGuide = gitCloneGuide(gitUrl);
	const overSsh = looksLikeSshUrl(gitUrl);

	const sourceMissing =
		target === "image"
			? image.trim() === ""
			: accountMode
				? gitSel === null || gitSel.repo === "" || gitSel.branch === ""
				: gitUrl.trim() === "" || gitBranch.trim() === "";

	const stage = useMutation({
		mutationFn: async () => {
			const body: StageServiceSourceChangeInputBody =
				target === "image"
					? {
							sourceType: "image",
							image: image.trim(),
							...(registryUsername.trim() !== "" || registryPassword !== ""
								? {
										registryAuth: {
											username: registryUsername.trim(),
											password: registryPassword,
										},
									}
								: {}),
						}
					: {
							sourceType: "git",
							gitUrl:
								accountMode && gitSel
									? (gitSel.cloneUrl ?? `https://${gitSel.repo}.git`)
									: gitUrl.trim(),
							gitBranch:
								accountMode && gitSel ? gitSel.branch : gitBranch.trim(),
							buildType,
							...(!accountMode && !overSsh && gitToken !== ""
								? { gitToken }
								: {}),
						};
			await stageServiceSourceChange({
				path: { serviceId: service.id },
				headers: { "If-Match": `"${service.generation}"` },
				body,
			});
		},
		onSuccess: async () => {
			onClose();
			if (accountMode && gitSel) {
				try {
					await linkServiceGit({
						path: { serviceId: service.id },
						body: {
							connectionId: gitSel.connectionId,
							repo: gitSel.repo,
							branch: gitSel.branch,
						},
					});
				} catch (failure) {
					toast.warning(
						`${service.name} has no repository link`,
						`Deploy on push is off: ${errorMessage(failure)}`,
					);
				}
			}
			await invalidateServices(queryClient);
			toast.success("Source change staged", "Deploy to apply it.");
		},
	});

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (sourceMissing || stage.isPending) return;
		stage.mutate();
	};

	return (
		<form onSubmit={submit} className="contents">
			<DialogHeader>
				<DialogTitle>Change source</DialogTitle>
			</DialogHeader>
			<DialogBody>
				<Segmented<SourceKind>
					aria-label="New source"
					font="sans"
					density="regular"
					value={target}
					onValueChange={setTarget}
					options={[
						{
							value: "image",
							label: current === "image" ? "Image (current)" : "Image",
							disabled: current === "image",
						},
						{
							value: "git",
							label: current === "git" ? "Git (current)" : "Git",
							disabled: current === "git",
						},
					]}
				/>
				{target === "image" ? (
					<>
						<Field>
							<FieldLabel htmlFor="change-source-image">Image</FieldLabel>
							<Input
								id="change-source-image"
								scale="lg"
								autoFocus
								autoComplete="off"
								spellCheck={false}
								className="font-mono"
								placeholder="ghcr.io/acme/app:1.4"
								value={image}
								onChange={(event) => setImage(event.target.value)}
							/>
						</Field>
						{image.trim() !== "" ? (
							<CredentialHelp
								guide={registryGuide(image)}
								fieldIds={[
									"change-source-registry-username",
									"change-source-registry-password",
								]}
							/>
						) : null}
						<Field>
							<FieldLabel htmlFor="change-source-registry-username">
								Registry login (private)
							</FieldLabel>
							<div className="grid grid-cols-2 gap-2">
								<Input
									id="change-source-registry-username"
									scale="lg"
									aria-label="Registry username"
									autoComplete="off"
									placeholder="Username"
									value={registryUsername}
									onChange={(event) => setRegistryUsername(event.target.value)}
								/>
								<Input
									id="change-source-registry-password"
									scale="lg"
									aria-label="Registry password"
									type="password"
									autoComplete="new-password"
									placeholder="Password or token"
									value={registryPassword}
									onChange={(event) => setRegistryPassword(event.target.value)}
								/>
							</div>
						</Field>
					</>
				) : null}
				{target === "git" && hasAccounts ? (
					<Segmented<"account" | "url">
						aria-label="Where the repository comes from"
						font="sans"
						density="regular"
						value={gitMode}
						onValueChange={setGitMode}
						options={[
							{ value: "account", label: "Connected account" },
							{ value: "url", label: "Any URL" },
						]}
					/>
				) : null}
				{accountMode ? (
					<RepoPicker
						idPrefix="change-source"
						autoFocus
						connections={gitConnections.data?.items ?? []}
						value={gitSel}
						onChange={setGitSel}
					/>
				) : null}
				{target === "git" && !accountMode ? (
					<>
						<Field>
							<FieldLabel htmlFor="change-source-git-url">
								Repository URL
							</FieldLabel>
							<Input
								id="change-source-git-url"
								scale="lg"
								autoFocus
								autoComplete="off"
								spellCheck={false}
								className="font-mono"
								placeholder="https://github.com/acme/shop.git"
								value={gitUrl}
								onChange={(event) => setGitUrl(event.target.value)}
							/>
						</Field>
						<Field>
							<FieldLabel htmlFor="change-source-git-branch">Branch</FieldLabel>
							<Input
								id="change-source-git-branch"
								scale="lg"
								autoComplete="off"
								className="font-mono"
								value={gitBranch}
								onChange={(event) => setGitBranch(event.target.value)}
							/>
						</Field>
						{overSsh ? null : (
							<>
								{gitGuide ? (
									<CredentialHelp
										guide={gitGuide}
										fieldIds={["change-source-git-token"]}
									/>
								) : null}
								<Field>
									<FieldLabel htmlFor="change-source-git-token">
										Access token (private)
									</FieldLabel>
									<Input
										id="change-source-git-token"
										scale="lg"
										type="password"
										autoComplete="new-password"
										placeholder="optional"
										value={gitToken}
										onChange={(event) => setGitToken(event.target.value)}
									/>
								</Field>
							</>
						)}
					</>
				) : null}
				{target === "git" ? (
					<Field>
						<FieldLabel htmlFor="change-source-build">Build method</FieldLabel>
						<Select
							value={buildType}
							items={BUILD_ITEMS}
							onValueChange={(next) => setBuildType(String(next ?? "auto"))}
						>
							<SelectTrigger id="change-source-build" className="w-full">
								<SelectValue />
							</SelectTrigger>
							<SelectContent alignItemWithTrigger={false}>
								{BUILD_TYPES.map((type) => (
									<SelectItem key={type.value} value={type.value}>
										{type.label}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						<FieldDescription>
							{BUILD_TYPES.find((type) => type.value === buildType)?.blurb}
						</FieldDescription>
					</Field>
				) : null}
				<p className="type-secondary">
					Domains, variables and volumes stay. Applies on the next deploy.
				</p>
				{stage.isError ? (
					<FieldError>{errorMessage(stage.error)}</FieldError>
				) : null}
			</DialogBody>
			<DialogFooter>
				<Button type="button" variant="ghost" onClick={onClose}>
					Cancel
				</Button>
				<Button type="submit" disabled={sourceMissing || stage.isPending}>
					{stage.isPending ? <Spinner /> : null}
					Stage change
				</Button>
			</DialogFooter>
		</form>
	);
}
