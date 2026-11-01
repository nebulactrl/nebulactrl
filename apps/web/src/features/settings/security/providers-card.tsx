import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRoundIcon, PlusIcon } from "lucide-react";
import { useState } from "react";
import { errorMessage } from "#/api/client-config";
import {
	getAuthSettingsOptions,
	getAuthSettingsQueryKey,
	listAuthProvidersOptions,
	listAuthProvidersQueryKey,
} from "#/api/gen/@tanstack/react-query.gen";
import { deleteAuthProvider, updateAuthSettings } from "#/api/gen/sdk.gen";
import type { ProviderBody } from "#/api/gen/types.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { DocsLink } from "#/components/docs/docs-link";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import {
	Card,
	CardDescription,
	CardHeader,
	CardTitle,
} from "#/components/ui/card";
import { FieldError } from "#/components/ui/field";
import { Skeleton } from "#/components/ui/skeleton";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import { ProviderDialog } from "./provider-dialog";

const KIND_LABEL = { oidc: "OpenID Connect", github: "GitHub" } as const;

/**
 * Instance sign-in methods, for the instance administrator: the identity
 * providers people can sign in with, and whether strangers may create an
 * account through them without an invite (off by default).
 */
export function ProvidersCard() {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const providers = useQuery(listAuthProvidersOptions());
	const settings = useQuery(getAuthSettingsOptions());
	const [dialog, setDialog] = useState<
		{ provider: ProviderBody | undefined } | undefined
	>();

	const setSignup = useMutation({
		mutationFn: (allowOpenSignup: boolean) =>
			updateAuthSettings({ body: { allowOpenSignup } }),
		onSuccess: async () => {
			await queryClient.invalidateQueries({
				queryKey: getAuthSettingsQueryKey(),
			});
		},
		onError: (error) =>
			toast.error("Could not change the setting", errorMessage(error)),
	});

	const remove = useMutation({
		mutationFn: (provider: ProviderBody) =>
			deleteAuthProvider({ path: { id: provider.id } }),
		onSuccess: async (_data, provider) => {
			toast.success(`Removed ${provider.name}`);
			await queryClient.invalidateQueries({
				queryKey: listAuthProvidersQueryKey(),
			});
		},
	});

	const ask = (provider: ProviderBody) =>
		void confirm({
			title: `Remove ${provider.name}?`,
			body: "People can no longer sign in with it, and every link between their accounts and this provider is deleted. Their NebulaCtrl accounts stay; anyone without a password needs an invite or a password reset to get back in.",
			cta: "Remove provider",
			danger: true,
			hold: false,
			onConfirm: async () => {
				await remove.mutateAsync(provider);
			},
		});

	const items = providers.data?.items ?? [];

	return (
		<Card>
			<CardHeader>
				<CardTitle className="flex items-center gap-2">
					<KeyRoundIcon aria-hidden="true" className="size-4 text-ink-3" />
					Single sign-on
				</CardTitle>
				<Button
					variant="primary"
					size="sm"
					onClick={() => setDialog({ provider: undefined })}
				>
					<PlusIcon />
					Connect your identity provider
				</Button>
			</CardHeader>
			<div className="flex flex-col gap-4 px-5 py-5">
				<CardDescription>
					Let people sign in with Google, Microsoft Entra ID, Okta, Auth0,
					authentik, Keycloak, Zitadel, Pocket ID, GitLab, any OpenID Connect
					provider, or GitHub. They are matched to an account by verified email;
					a person with no account needs an invite to a team.{" "}
					<DocsLink to="security/sso-setup">Setup guide</DocsLink>
				</CardDescription>
				{providers.isPending ? (
					<Skeleton className="h-16 rounded-ctl" />
				) : providers.isError ? (
					<FieldError>{errorMessage(providers.error)}</FieldError>
				) : items.length === 0 ? (
					<p className="surface-well rounded-ctl px-4 py-3 text-ink-3 text-xs">
						No provider yet. Passwords are the only way to sign in.
					</p>
				) : (
					<ul className="m-0 flex list-none flex-col divide-y divide-line p-0">
						{items.map((provider) => (
							<li
								key={provider.id}
								className="flex items-center gap-3 py-3 first:pt-0 last:pb-0"
							>
								<span className="flex min-w-0 flex-1 flex-col gap-1">
									<span className="flex flex-wrap items-center gap-2">
										<span className="font-medium text-ink-1 text-sm">
											{provider.name}
										</span>
										<Badge
											tone={provider.enabled ? "ok" : "off"}
											lamp
											shape="capsule"
										>
											{provider.enabled ? "Enabled" : "Disabled"}
										</Badge>
										{provider.trustMfa ? (
											<Badge tone="info" shape="capsule">
												Trusted MFA
											</Badge>
										) : null}
									</span>
									<span className="truncate text-2xs text-ink-3">
										{KIND_LABEL[provider.kind]}
										{provider.issuerUrl ? (
											<span className="font-mono">{` · ${provider.issuerUrl}`}</span>
										) : null}
										{(provider.allowedDomains ?? []).length > 0
											? ` · ${(provider.allowedDomains ?? []).join(", ")}`
											: " · any verified email"}
									</span>
								</span>
								<Button
									variant="ghost"
									size="sm"
									onClick={() => setDialog({ provider })}
								>
									Edit
								</Button>
								<Button
									variant="ghost"
									size="sm"
									className="text-destructive-text"
									onClick={() => ask(provider)}
								>
									Remove
								</Button>
							</li>
						))}
					</ul>
				)}
				<div className="flex items-start justify-between gap-4 border-line border-t pt-4">
					<div className="flex min-w-0 flex-col gap-0.5">
						<label
							htmlFor="open-signup"
							className="font-medium text-ink-1 text-sm"
						>
							Allow open sign-up
						</label>
						<span className="max-w-prose text-pretty text-ink-3 text-xs leading-relaxed">
							Anyone who can sign in with a provider above gets an account
							without an invite. Keep it off to allow sign-up only through a
							team invite. Allowed email domains still apply.
						</span>
					</div>
					<Switch
						id="open-signup"
						checked={settings.data?.allowOpenSignup ?? false}
						disabled={settings.isPending || setSignup.isPending}
						onCheckedChange={(next) => setSignup.mutate(next)}
					/>
				</div>
			</div>
			<ProviderDialog
				open={dialog !== undefined}
				onOpenChange={(open) => {
					if (!open) setDialog(undefined);
				}}
				provider={dialog?.provider}
			/>
		</Card>
	);
}
