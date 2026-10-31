import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeftIcon, ExternalLinkIcon } from "lucide-react";
import { type FormEvent, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { getSystemOptions } from "#/api/gen/@tanstack/react-query.gen";
import { createGitConnection, startGitHubAppManifest } from "#/api/gen/sdk.gen";
import type { CreateConnectionResult } from "#/api/gen/types.gen";
import { CopyButton } from "#/components/common/copy-button";
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
import { toast } from "#/components/ui/toast";
import { gitProviderGuide } from "#/lib/credential-links";
import {
	type GitProvider,
	PROVIDERS,
	providerInfo,
	submitManifestForm,
} from "./model";
import { ProviderIcon } from "./provider-icon";
import { invalidateGit } from "./queries";
import { WebhookPanel } from "./webhook-panel";

type AuthChoice = "token" | "oauth";

function ProviderPicker({ onPick }: { onPick(id: GitProvider): void }) {
	return (
		<ul className="m-0 grid list-none grid-cols-1 gap-2 p-0 sm:grid-cols-2">
			{PROVIDERS.map((p) => (
				<li key={p.id}>
					<button
						type="button"
						onClick={() => onPick(p.id)}
						className="surface-raised pressable flex h-full w-full cursor-pointer items-start gap-3 rounded-card p-3 text-left outline-none hover:border-line-control focus-visible:focus-ring"
					>
						<ProviderIcon provider={p.id} size={36} />
						<span className="flex min-w-0 flex-col gap-0.5">
							<span className="font-medium text-ink-1 text-sm">{p.label}</span>
							<span className="text-ink-3 text-xs leading-snug">{p.blurb}</span>
						</span>
					</button>
				</li>
			))}
		</ul>
	);
}

function GitHubForm({ onClose }: { onClose(): void }) {
	const [owner, setOwner] = useState("");
	const [baseUrl, setBaseUrl] = useState("");
	const [error, setError] = useState<string | null>(null);
	const start = useMutation({
		mutationFn: async () => {
			const { data } = await startGitHubAppManifest({
				body: {
					...(owner.trim() ? { owner: owner.trim() } : {}),
					...(baseUrl.trim() ? { baseUrl: baseUrl.trim() } : {}),
				},
			});
			return data;
		},
		onSuccess: (manifest) => {
			// GitHub takes the manifest as a form POST: the page leaves for github.com.
			submitManifestForm(manifest.postUrl, manifest.manifest);
		},
		onError: (e) => setError(errorMessage(e)),
	});
	const submit = (event: FormEvent) => {
		event.preventDefault();
		setError(null);
		start.mutate();
	};
	return (
		<form onSubmit={submit} className="contents">
			<DialogBody>
				<p className="m-0 text-ink-2 text-sm leading-relaxed">
					NebulaCtrl creates a GitHub App for you. You confirm it on GitHub,
					install it on the repositories you want, and come back here. The App
					gets read access to code and metadata, write access to pull requests
					and commit statuses, and sends push and pull request events.
				</p>
				<Field>
					<FieldLabel htmlFor="gh-owner">Organization (optional)</FieldLabel>
					<Input
						id="gh-owner"
						autoComplete="off"
						spellCheck={false}
						placeholder="Leave empty to own the App yourself"
						value={owner}
						onChange={(e) => setOwner(e.target.value)}
					/>
					<FieldDescription>
						The App is created in this organization's settings. You need to be
						an owner.
					</FieldDescription>
				</Field>
				<Field>
					<FieldLabel htmlFor="gh-base">
						GitHub Enterprise Server URL (optional)
					</FieldLabel>
					<Input
						id="gh-base"
						autoComplete="off"
						spellCheck={false}
						className="font-mono"
						placeholder="https://github.com"
						value={baseUrl}
						onChange={(e) => setBaseUrl(e.target.value)}
					/>
				</Field>
				{error ? (
					<Alert variant="destructive">
						<AlertTitle>Could not start</AlertTitle>
						<AlertDescription>{error}</AlertDescription>
					</Alert>
				) : null}
			</DialogBody>
			<DialogFooter>
				<Button type="button" variant="secondary" onClick={onClose}>
					Cancel
				</Button>
				<Button type="submit" variant="primary" disabled={start.isPending}>
					{start.isPending ? <Spinner /> : <ExternalLinkIcon />}
					Create the GitHub App
				</Button>
			</DialogFooter>
		</form>
	);
}

function TokenOrOAuthForm({
	provider,
	onClose,
	onCreated,
}: {
	provider: GitProvider;
	onClose(): void;
	onCreated(result: CreateConnectionResult): void;
}) {
	const info = providerInfo(provider);
	const system = useQuery({ ...getSystemOptions(), staleTime: 60_000 });
	const publicUrl = (system.data?.publicUrl ?? "") || window.location.origin;
	const bitbucket = provider === "bitbucket";
	const [auth, setAuth] = useState<AuthChoice>("token");
	const [name, setName] = useState("");
	const [baseUrl, setBaseUrl] = useState("");
	const [token, setToken] = useState("");
	const [clientId, setClientId] = useState("");
	const [clientSecret, setClientSecret] = useState("");
	const [error, setError] = useState<string | null>(null);
	const redirectUri = `${publicUrl.replace(/\/$/, "")}/api/v1/git/oauth/callback`;
	const guide = gitProviderGuide(provider, baseUrl);

	const create = useMutation({
		mutationFn: async () => {
			const { data } = await createGitConnection({
				body: {
					provider: provider as "gitlab" | "gitea" | "forgejo" | "bitbucket",
					...(name.trim() ? { name: name.trim() } : {}),
					...(baseUrl.trim() ? { baseUrl: baseUrl.trim() } : {}),
					...(bitbucket
						? token.trim()
							? { token: token.trim() }
							: {}
						: auth === "token"
							? { authType: "token" as const, token: token.trim() }
							: {
									authType: "oauth" as const,
									clientId: clientId.trim(),
									clientSecret: clientSecret.trim(),
								}),
				},
			});
			return data;
		},
		onSuccess: (result) => {
			if (result.authorizeUrl) {
				// Hand over to the provider; it redirects back to Settings.
				window.location.assign(result.authorizeUrl);
				return;
			}
			onCreated(result);
		},
		onError: (e) => setError(errorMessage(e)),
	});

	const missing =
		(info.instanceUrlRequired && baseUrl.trim() === "") ||
		(!bitbucket && auth === "token" && token.trim() === "") ||
		(!bitbucket &&
			auth === "oauth" &&
			(clientId.trim() === "" || clientSecret.trim() === ""));

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (missing || create.isPending) return;
		setError(null);
		create.mutate();
	};

	return (
		<form onSubmit={submit} className="contents">
			<DialogBody>
				<Field>
					<FieldLabel htmlFor="git-name">Name</FieldLabel>
					<Input
						id="git-name"
						autoComplete="off"
						placeholder={`${info.label}${info.defaultBaseUrl ? "" : " (my instance)"}`}
						value={name}
						onChange={(e) => setName(e.target.value)}
					/>
				</Field>
				{bitbucket ? null : (
					<Field>
						<FieldLabel htmlFor="git-base">
							{info.instanceUrlRequired
								? "Instance URL"
								: "Instance URL (self-hosted only)"}
						</FieldLabel>
						<Input
							id="git-base"
							autoComplete="off"
							spellCheck={false}
							className="font-mono"
							placeholder={info.defaultBaseUrl || "https://git.example.com"}
							value={baseUrl}
							onChange={(e) => setBaseUrl(e.target.value)}
						/>
					</Field>
				)}
				{bitbucket ? (
					<>
						{guide ? (
							<CredentialHelp guide={guide} fieldIds={["git-token"]} />
						) : null}
						<Field>
							<FieldLabel htmlFor="git-token">
								Access token (optional)
							</FieldLabel>
							<Input
								id="git-token"
								type="password"
								autoComplete="new-password"
								value={token}
								onChange={(e) => setToken(e.target.value)}
							/>
							<FieldDescription>
								Optional. Without it only public repositories build.
							</FieldDescription>
						</Field>
					</>
				) : (
					<>
						<Segmented<AuthChoice>
							aria-label="How NebulaCtrl signs in"
							font="sans"
							density="regular"
							value={auth}
							onValueChange={setAuth}
							options={[
								{ value: "token", label: "Access token" },
								{ value: "oauth", label: "OAuth application" },
							]}
						/>
						{auth === "token" ? (
							<>
								{guide ? (
									<CredentialHelp guide={guide} fieldIds={["git-token"]} />
								) : null}
								<Field>
									<FieldLabel htmlFor="git-token">Access token</FieldLabel>
									<Input
										id="git-token"
										type="password"
										autoComplete="new-password"
										value={token}
										onChange={(e) => setToken(e.target.value)}
									/>
								</Field>
							</>
						) : (
							<>
								<div className="surface-well flex flex-col gap-2 rounded-ctl p-3">
									<span className="text-ink-2 text-xs leading-relaxed">
										Create an OAuth application at your {info.label} instance
										and give it this redirect URI:
									</span>
									<div className="flex items-start gap-2">
										<code className="min-w-0 flex-1 select-all break-all font-mono text-ink-1 text-xs leading-[1.7]">
											{redirectUri}
										</code>
										<CopyButton variant="chip" value={redirectUri} />
									</div>
									{provider === "gitlab" ? (
										<span className="text-ink-3 text-xs">
											Scopes: api and read_repository. Keep it Confidential.
										</span>
									) : null}
								</div>
								<div className="grid grid-cols-2 gap-3">
									<Field>
										<FieldLabel htmlFor="git-client-id">Client ID</FieldLabel>
										<Input
											id="git-client-id"
											autoComplete="off"
											spellCheck={false}
											className="font-mono"
											value={clientId}
											onChange={(e) => setClientId(e.target.value)}
										/>
									</Field>
									<Field>
										<FieldLabel htmlFor="git-client-secret">
											Client secret
										</FieldLabel>
										<Input
											id="git-client-secret"
											type="password"
											autoComplete="new-password"
											value={clientSecret}
											onChange={(e) => setClientSecret(e.target.value)}
										/>
									</Field>
								</div>
							</>
						)}
					</>
				)}
				{error ? <FieldError>{error}</FieldError> : null}
			</DialogBody>
			<DialogFooter>
				<Button type="button" variant="secondary" onClick={onClose}>
					Cancel
				</Button>
				<Button
					type="submit"
					variant="primary"
					disabled={missing || create.isPending}
				>
					{create.isPending ? <Spinner /> : null}
					{!bitbucket && auth === "oauth" ? "Save and authorize" : "Connect"}
				</Button>
			</DialogFooter>
		</form>
	);
}

/**
 * Connect a git provider: pick it, then GitHub gets its one-click App flow,
 * GitLab, Gitea and Forgejo a token or an OAuth application (always with the
 * instance URL), and Bitbucket the hand-made webhook.
 */
export function ConnectDialog({
	open,
	onOpenChange,
}: {
	open: boolean;
	onOpenChange(open: boolean): void;
}) {
	const queryClient = useQueryClient();
	const [provider, setProvider] = useState<GitProvider | null>(null);
	const [created, setCreated] = useState<CreateConnectionResult | null>(null);

	const close = () => {
		onOpenChange(false);
		setTimeout(() => {
			setProvider(null);
			setCreated(null);
		}, 250);
	};

	const info = provider ? providerInfo(provider) : null;
	const title = created
		? `Connected ${created.connection.name}`
		: info
			? `Connect ${info.label}`
			: "Connect a git provider";

	return (
		<Dialog
			open={open}
			onOpenChange={(next) => (next ? onOpenChange(true) : close())}
		>
			<DialogContent size="lg">
				<DialogHeader>
					<DialogTitle className="flex items-center gap-2.5">
						{info && !created ? (
							<Button
								variant="ghost"
								size="icon-sm"
								aria-label="Back to providers"
								onClick={() => setProvider(null)}
							>
								<ArrowLeftIcon />
							</Button>
						) : null}
						{title}
					</DialogTitle>
					<DialogDescription>
						{created
							? "Link a service to a repository to deploy on every push."
							: "Repositories you pick later deploy on push, report their status back and can get pull request previews."}
					</DialogDescription>
				</DialogHeader>
				{created ? (
					<>
						<DialogBody>
							{created.connection.manualWebhook ? (
								<WebhookPanel
									url={created.connection.webhookUrl}
									secret={created.webhookSecret}
								/>
							) : (
								<p className="m-0 text-ink-2 text-sm">
									NebulaCtrl registers the repository webhook itself when you
									link a service.
								</p>
							)}
						</DialogBody>
						<DialogFooter>
							<Button variant="primary" onClick={close}>
								{created.connection.manualWebhook
									? "I have created the webhook"
									: "Done"}
							</Button>
						</DialogFooter>
					</>
				) : provider === null ? (
					<DialogBody>
						<ProviderPicker onPick={setProvider} />
					</DialogBody>
				) : provider === "github" ? (
					<GitHubForm onClose={close} />
				) : (
					<TokenOrOAuthForm
						provider={provider}
						onClose={close}
						onCreated={(result) => {
							setCreated(result);
							toast.success(
								`Connected ${result.connection.name}`,
								result.connection.accountLogin
									? `Signed in as ${result.connection.accountLogin}.`
									: "The connection is ready.",
							);
							void invalidateGit(queryClient);
						}}
					/>
				)}
			</DialogContent>
		</Dialog>
	);
}
