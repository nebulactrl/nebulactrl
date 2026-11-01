import { useState } from "react";
import type { Service } from "#/api/models";
import { CredentialHelp } from "#/components/common/credential-help";
import { InfoTip } from "#/components/settings/info-tip";
import { MoreOptions } from "#/components/settings/more-options";
import { Button } from "#/components/ui/button";
import { CheckLabel } from "#/components/ui/check-label";
import { Input } from "#/components/ui/input";
import { GitLinkRows } from "#/features/git/git-link-section";
import { gitCloneGuide, registryGuide } from "#/lib/credential-links";
import { BuildPlanPreview } from "./build-plan-preview";
import { BUILD_TYPES } from "./build-types";
import { ChangeSourceDialog } from "./change-source-dialog";
import {
	type DraftProps,
	Section,
	SelectRow,
	SettingRow,
	TextRow,
} from "./fields";
import { registryHelpOpen } from "./settings-chrome";
import { SshKeyRows } from "./ssh-key-rows";
import { looksLikeSshUrl } from "./ssh-model";

/**
 * Switching between an image and a repository is staged like any setting, but
 * it replaces the whole source, so it waits for a clean draft: the form's
 * unsaved edits belong to the old source.
 */
function ChangeSourceRow({
	service,
	dirty,
}: {
	service: Service;
	dirty: boolean;
}) {
	const [open, setOpen] = useState(false);
	const switchable =
		(service.kind === "app" || service.kind === "worker") &&
		(service.sourceType === "image" || service.sourceType === "git");
	if (!switchable) return null;
	return (
		<SettingRow
			anchor="change-source"
			label="Source type"
			description={
				dirty
					? "Save or discard your edits first."
					: service.sourceType === "image"
						? "Switch to a Git repository."
						: "Switch to a Docker image."
			}
		>
			<div>
				<Button
					variant="secondary"
					disabled={dirty}
					onClick={() => setOpen(true)}
				>
					Change source…
				</Button>
			</div>
			<ChangeSourceDialog
				service={service}
				open={open}
				onOpenChange={setOpen}
			/>
		</SettingRow>
	);
}

/** Where the code or image comes from. Credentials are write-only. */
export function SourceSection({
	service,
	draft,
	errors,
	set,
	dirty = false,
}: DraftProps & { service: Service; dirty?: boolean }) {
	const gitGuide = gitCloneGuide(draft.gitUrl);
	const overSsh = looksLikeSshUrl(draft.gitUrl);
	if (service.sourceType === "image") {
		return (
			<Section
				title="Source"
				description={
					<>
						A Docker image pulled onto the server.
						<InfoTip
							label="About the image source"
							className="ml-1 align-text-bottom"
						>
							The digest it resolves to is recorded with every release.
						</InfoTip>
					</>
				}
			>
				<ChangeSourceRow service={service} dirty={dirty} />
				<TextRow
					id="set-image"
					field="config.image"
					label="Image"
					description="repository:tag"
					mono
					value={draft.image}
					error={errors.image}
					placeholder="traefik/whoami:latest"
					onChange={(value) => set("image", value)}
				/>
				<SettingRow
					anchor="registry-login"
					label="Registry login"
					description="For a private registry. Stored encrypted, never shown."
				>
					<div className="flex w-full flex-col gap-2">
						<MoreOptions
							label="How to create a registry token"
							defaultOpen={registryHelpOpen({
								authSet: service.registryAuthSet,
								username: draft.registryUsername,
								password: draft.registryPassword,
							})}
						>
							<CredentialHelp
								guide={registryGuide(draft.image)}
								fieldIds={["registry-username", "registry-password"]}
								className="py-2"
							/>
						</MoreOptions>
						<div className="grid grid-cols-2 gap-2">
							<Input
								id="registry-username"
								aria-label="Registry username"
								autoComplete="off"
								placeholder={
									service.registryAuthSet ? "Set — type to replace" : "Username"
								}
								value={draft.registryUsername}
								onChange={(event) =>
									set("registryUsername", event.target.value)
								}
							/>
							<Input
								id="registry-password"
								aria-label="Registry password"
								type="password"
								autoComplete="new-password"
								placeholder={
									service.registryAuthSet
										? "Set — type to replace"
										: "Password or token"
								}
								value={draft.registryPassword}
								onChange={(event) =>
									set("registryPassword", event.target.value)
								}
							/>
						</div>
						{service.registryAuthSet ? (
							<CheckLabel
								checked={draft.clearRegistryAuth}
								onCheckedChange={(checked) => set("clearRegistryAuth", checked)}
								labelClassName="type-secondary"
							>
								Remove the stored credentials
							</CheckLabel>
						) : null}
					</div>
				</SettingRow>
			</Section>
		);
	}
	return (
		<Section
			title="Source"
			description={
				<>
					A Git repository; each deploy builds the head of the branch.
					<InfoTip
						label="About the repository source"
						className="ml-1 align-text-bottom"
					>
						Built with its Dockerfile when it has one, otherwise with Railpack.
					</InfoTip>
				</>
			}
		>
			<ChangeSourceRow service={service} dirty={dirty} />
			<GitLinkRows service={service} dirty={dirty} />
			<TextRow
				id="set-git-url"
				field="config.gitUrl"
				label="Repository"
				description="HTTPS, or SSH with a deploy key."
				info="For SSH, use ssh://git@host/org/repo.git or git@host:org/repo.git."
				mono
				value={draft.gitUrl}
				error={errors.gitUrl}
				placeholder="https://github.com/acme/shop.git"
				onChange={(value) => set("gitUrl", value)}
			/>
			<TextRow
				id="set-git-branch"
				field="config.gitBranch"
				label="Branch"
				mono
				value={draft.gitBranch}
				error={errors.gitBranch}
				placeholder="main"
				onChange={(value) => set("gitBranch", value)}
			/>
			<SelectRow
				id="set-build-type"
				field="config.buildType"
				label="Build method"
				description={
					BUILD_TYPES.find((t) => t.value === draft.buildType)?.blurb
				}
				info={BUILD_TYPES.find((t) => t.value === draft.buildType)?.info}
				value={draft.buildType}
				options={BUILD_TYPES}
				onChange={(value) => set("buildType", value)}
			/>
			<BuildPlanPreview service={service} dirty={dirty} />
			{overSsh ? <SshKeyRows service={service} /> : null}
			{overSsh ? null : (
				<SettingRow
					anchor="git-token"
					label="Access token"
					description="For a private repository. Write-only: stored encrypted, never shown again."
				>
					<div className="flex w-full flex-col gap-2">
						{gitGuide ? (
							<CredentialHelp guide={gitGuide} fieldIds={["git-token-input"]} />
						) : null}
						<Input
							id="git-token-input"
							aria-label="Git access token"
							type="password"
							autoComplete="new-password"
							placeholder={
								service.gitTokenSet ? "Set — type to replace" : "Not set"
							}
							value={draft.gitToken}
							onChange={(event) => set("gitToken", event.target.value)}
						/>
						{service.gitTokenSet ? (
							<CheckLabel
								checked={draft.clearGitToken}
								onCheckedChange={(checked) => set("clearGitToken", checked)}
								labelClassName="type-secondary"
							>
								Remove the stored token
							</CheckLabel>
						) : null}
					</div>
				</SettingRow>
			)}
		</Section>
	);
}

export function ResourcesSection({ draft, errors, set }: DraftProps) {
	return (
		<Section
			title="Limits"
			aside="Applies on next deploy"
			description="Memory and CPUs the container may use. Empty means no limit."
		>
			<TextRow
				id="set-memory"
				field="config.memoryMb"
				label="Memory (MB)"
				type="number"
				narrow
				value={draft.memoryMb}
				error={errors.memoryMb}
				placeholder="unlimited"
				onChange={(value) => set("memoryMb", value)}
			/>
			<TextRow
				id="set-cpus"
				field="config.cpus"
				label="CPUs"
				description="Fractions allowed, e.g. 0.5"
				type="number"
				narrow
				value={draft.cpus}
				error={errors.cpus}
				placeholder="unlimited"
				onChange={(value) => set("cpus", value)}
			/>
		</Section>
	);
}

/** The service name; its slug (private host) never changes. */
export function NameRow({ draft, errors, set }: DraftProps) {
	return (
		<TextRow
			id="set-name"
			field="name"
			label="Name"
			value={draft.name}
			error={errors.name}
			onChange={(value) => set("name", value)}
		/>
	);
}
