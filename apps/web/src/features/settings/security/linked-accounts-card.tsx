import { useMutation, useQueryClient } from "@tanstack/react-query";
import { KeyRoundIcon } from "lucide-react";
import { type FormEvent, useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { getMySecurityQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import { startIdentityLink, unlinkIdentity } from "#/api/gen/sdk.gen";
import type { IdentityBody } from "#/api/gen/types.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { Relative } from "#/components/common/relative";
import { Button } from "#/components/ui/button";
import {
	Card,
	CardDescription,
	CardFooter,
	CardHeader,
	CardTitle,
} from "#/components/ui/card";
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
import { linkLabel, unlinkedProviders } from "./link-sso";

type Identity = IdentityBody;

export interface SsoProvider {
	id: string;
	name: string;
}

/**
 * Single sign-on: the accounts linked to this user (each with an Unlink
 * action) and, while a provider is set up, the way to link one. A provider
 * never links itself to an account by email at sign-in: this is the only door,
 * and it asks for the password again.
 */
export function LinkedAccountsCard({
	identities,
	providers,
	hasPassword,
}: {
	identities: readonly Identity[];
	providers: readonly SsoProvider[];
	hasPassword: boolean;
}) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const [linking, setLinking] = useState<SsoProvider | null>(null);
	const unlink = useMutation({
		mutationFn: (identity: Identity) =>
			unlinkIdentity({ path: { id: identity.id } }),
		onSuccess: async (_data, identity) => {
			toast.success(`Unlinked ${identity.providerName}`);
			await queryClient.invalidateQueries({
				queryKey: getMySecurityQueryKey(),
			});
		},
		onError: (error) =>
			toast.error("Could not unlink the account", errorMessage(error)),
	});

	const ask = (identity: Identity) =>
		void confirm({
			title: `Unlink ${identity.providerName}?`,
			body: "You will no longer be able to sign in with it. Your NebulaCtrl account and its teams stay as they are. You can link it again here.",
			cta: "Unlink",
			danger: true,
			hold: false,
			onConfirm: async () => {
				await unlink.mutateAsync(identity);
			},
		});

	const linkable = unlinkedProviders(providers, identities);

	return (
		<Card>
			<CardHeader>
				<CardTitle>Single sign-on</CardTitle>
			</CardHeader>
			<div className="flex flex-col gap-1 px-5 py-4">
				<CardDescription className="pb-2">
					{identities.length > 0
						? "Accounts at an identity provider that can sign in as you."
						: "Link an account at your identity provider to sign in with it."}
				</CardDescription>
				{identities.length > 0 ? (
					<ul className="m-0 flex list-none flex-col divide-y divide-line p-0">
						{identities.map((identity) => (
							<li
								key={identity.id}
								className="flex items-center gap-3 py-2.5 first:pt-0 last:pb-0"
							>
								<KeyRoundIcon
									aria-hidden="true"
									className="size-4 shrink-0 text-ink-3"
								/>
								<span className="flex min-w-0 flex-1 flex-col gap-0.5">
									<span className="truncate font-medium text-ink-1 text-sm">
										{identity.providerName}
									</span>
									<span className="truncate font-mono text-2xs text-ink-3">
										{identity.email}
										{identity.lastLoginAt ? (
											<>
												{" · last used "}
												<Relative iso={identity.lastLoginAt} />
											</>
										) : null}
									</span>
								</span>
								<Button
									variant="ghost"
									size="sm"
									className="text-destructive-text"
									onClick={() => ask(identity)}
								>
									Unlink
								</Button>
							</li>
						))}
					</ul>
				) : null}
			</div>
			{linkable.length > 0 ? (
				<CardFooter>
					{linkable.map((provider) => (
						<Button
							key={provider.id}
							variant="secondary"
							onClick={() => setLinking(provider)}
						>
							{linkLabel(provider, linkable.length)}
						</Button>
					))}
				</CardFooter>
			) : null}
			<LinkDialog
				provider={linking}
				hasPassword={hasPassword}
				onOpenChange={(open) => {
					if (!open) setLinking(null);
				}}
			/>
		</Card>
	);
}

/**
 * Confirm it is you (the password, when the account has one), then go to the
 * provider; it sends the browser back to this page with the result.
 */
function LinkDialog({
	provider,
	hasPassword,
	onOpenChange,
}: {
	provider: SsoProvider | null;
	hasPassword: boolean;
	onOpenChange: (open: boolean) => void;
}) {
	const [password, setPassword] = useState("");
	useEffect(() => {
		if (provider) setPassword("");
	}, [provider]);

	const start = useMutation({
		mutationFn: async () => {
			if (!provider) throw new Error("No provider chosen");
			const { data } = await startIdentityLink({
				body: {
					providerId: provider.id,
					...(hasPassword ? { password } : {}),
				},
			});
			return data.url;
		},
		// A full page load: the provider's own sign-in page takes over.
		onSuccess: (url) => window.location.assign(url),
	});

	const ready = !hasPassword || password !== "";
	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (!ready || start.isPending) return;
		start.mutate();
	};

	return (
		<Dialog open={provider !== null} onOpenChange={onOpenChange}>
			<DialogContent size="sm">
				<form onSubmit={submit} className="contents">
					<DialogHeader>
						<DialogTitle>
							{provider ? `Link ${provider.name}` : "Link"}
						</DialogTitle>
						<DialogDescription>
							You will sign in at the provider, then come back here. The account
							you choose there can sign in as you from now on.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						{hasPassword ? (
							<Field>
								<FieldLabel htmlFor="sso-link-password">Password</FieldLabel>
								<Input
									id="sso-link-password"
									type="password"
									scale="lg"
									autoComplete="current-password"
									autoFocus
									value={password}
									onChange={(event) => setPassword(event.target.value)}
								/>
								<FieldDescription>
									Confirm it is you before a new way to sign in is added.
								</FieldDescription>
							</Field>
						) : null}
						{start.isError ? (
							<FieldError>{errorMessage(start.error)}</FieldError>
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
							disabled={!ready || start.isPending}
						>
							{start.isPending ? <Spinner /> : null}
							Continue
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
