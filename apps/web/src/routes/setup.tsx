import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, useRouter } from "@tanstack/react-router";
import { type FormEvent, useEffect, useState } from "react";
import { errorMessage, errorStatus } from "#/api/client-config";
import { login, setup as postSetup } from "#/api/gen/sdk.gen";
import { meQuery, setupQuery } from "#/api/hooks";
import { AuthNotice } from "#/components/auth/auth-notice";
import { AuthCard } from "#/components/auth/auth-screen";
import { Button } from "#/components/ui/button";
import { Field, FieldDescription, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Spinner } from "#/components/ui/spinner";
import { useDocumentTitle } from "#/lib/hooks/use-document-title";

export const Route = createFileRoute("/setup")({
	// The installer prints /setup?token=…; the token is read once and removed
	// from the address bar so it does not linger in history or referrers.
	validateSearch: (search: Record<string, unknown>): { token?: string } =>
		typeof search.token === "string" ? { token: search.token } : {},
	component: SetupPage,
});

const MIN_PASSWORD = 10;

function SetupPage() {
	const router = useRouter();
	const queryClient = useQueryClient();
	const [name, setName] = useState("");
	const [email, setEmail] = useState("");
	const [password, setPassword] = useState("");
	const search = Route.useSearch();
	const [setupToken, setSetupToken] = useState(search.token ?? "");
	const { data: status } = useQuery(setupQuery());
	const tokenRequired = status?.tokenRequired === true;
	useDocumentTitle(["Set up"]);

	useEffect(() => {
		if (search.token === undefined) return;
		void router.navigate({ to: "/setup", search: {}, replace: true });
	}, [router, search.token]);

	const setup = useMutation({
		mutationFn: async () => {
			const credentials = { email: email.trim(), password };
			await postSetup({
				body: {
					...credentials,
					name: name.trim(),
					...(tokenRequired ? { setupToken: setupToken.trim() } : {}),
				},
			});
			// Whether or not setup itself signs the admin in, this makes sure it.
			await login({ body: credentials });
		},
		onSuccess: async () => {
			queryClient.setQueryData(setupQuery().queryKey, {
				needsSetup: false,
				tokenRequired: false,
			});
			await queryClient.invalidateQueries({ queryKey: meQuery().queryKey });
			await router.invalidate();
			await router.navigate({ to: "/" });
		},
	});

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (setup.isPending || !ready) return;
		setup.mutate();
	};

	const failure = setup.error
		? errorStatus(setup.error) === 409
			? "This installation already has an administrator. Sign in instead."
			: errorStatus(setup.error) === 403
				? "The setup token is missing or wrong. Use the setup link the installer printed."
				: errorMessage(setup.error)
		: undefined;
	const ready =
		name.trim() !== "" &&
		email.trim() !== "" &&
		password.length >= MIN_PASSWORD &&
		(!tokenRequired || setupToken.trim() !== "");

	return (
		<AuthCard>
			<form onSubmit={submit} className="flex flex-col gap-4">
				<div className="mt-1.5 mb-1 flex flex-col gap-2">
					<h1 className="type-page-title text-[24px] text-ink-1">
						Set up this installation
					</h1>
					<span className="font-mono text-ink-3 text-xs leading-none">
						{window.location.host}
					</span>
					<span className="type-body text-pretty text-ink-3">
						Create the administrator account for this installation. It can only
						be done once.
					</span>
				</div>
				{failure ? <AuthNotice tone="destructive">{failure}</AuthNotice> : null}
				{tokenRequired ? (
					<Field>
						<FieldLabel htmlFor="setup-token">Setup token</FieldLabel>
						<Input
							id="setup-token"
							scale="xl"
							className="font-mono"
							autoComplete="off"
							spellCheck={false}
							autoFocus={setupToken === ""}
							required
							pattern=".*\S.*"
							title="Paste the setup token the installer printed"
							value={setupToken}
							onChange={(event) => setSetupToken(event.target.value)}
						/>
						<FieldDescription>
							Printed by the installer, and part of its setup link. On a manual
							install it is in the server log: run{" "}
							<code className="font-mono text-[11px]">
								docker compose logs nebulactrl
							</code>{" "}
							and look for the “first-run setup” line. It proves you are the
							person who installed this.
						</FieldDescription>
					</Field>
				) : null}
				<Field>
					<FieldLabel htmlFor="setup-name">Your name</FieldLabel>
					<Input
						id="setup-name"
						scale="xl"
						autoComplete="name"
						autoFocus={!tokenRequired || setupToken !== ""}
						required
						pattern=".*\S.*"
						title="Enter your name"
						value={name}
						onChange={(event) => setName(event.target.value)}
					/>
				</Field>
				<Field>
					<FieldLabel htmlFor="setup-email">Email</FieldLabel>
					<Input
						id="setup-email"
						scale="xl"
						type="email"
						autoComplete="username"
						required
						value={email}
						onChange={(event) => setEmail(event.target.value)}
					/>
				</Field>
				<Field>
					<FieldLabel htmlFor="setup-password">Password</FieldLabel>
					<Input
						id="setup-password"
						scale="xl"
						type="password"
						autoComplete="new-password"
						minLength={MIN_PASSWORD}
						required
						value={password}
						onChange={(event) => setPassword(event.target.value)}
					/>
					<FieldDescription>
						At least {MIN_PASSWORD} characters.
					</FieldDescription>
				</Field>
				<Button
					type="submit"
					size="xl"
					className="w-full"
					disabled={setup.isPending}
				>
					{setup.isPending ? <Spinner /> : null}
					Create administrator
				</Button>
			</form>
		</AuthCard>
	);
}
