import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, type ReactNode, useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { getInstanceQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import { updateInstance } from "#/api/gen/sdk.gen";
import type { InstanceSettings } from "#/api/gen/types.gen";
import { ErrorState } from "#/components/common/error-state";
import { Button } from "#/components/ui/button";
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
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import { SettingsNotice, SettingsPlate } from "#/features/settings/section";
import { ProvidersCard } from "#/features/settings/security/providers-card";
import { instanceQuery } from "./queries";

type Tls = NonNullable<InstanceSettings["smtpTls"]>;

const TLS_OPTIONS: readonly { value: Tls; label: string }[] = [
	{ value: "starttls", label: "STARTTLS (port 587)" },
	{ value: "implicit", label: "TLS on connect (port 465)" },
	{ value: "none", label: "None (port 25, trusted networks only)" },
];

const DEFAULT_PORT: Record<Tls, string> = {
	starttls: "587",
	implicit: "465",
	none: "25",
};

/** The instance settings and the one mutation that changes them. */
function useInstanceSettings() {
	const queryClient = useQueryClient();
	const instance = useQuery(instanceQuery());
	const save = useMutation({
		mutationFn: (body: Parameters<typeof updateInstance>[0]["body"]) =>
			updateInstance({ body }),
		onSuccess: async () => {
			await queryClient.invalidateQueries({ queryKey: getInstanceQueryKey() });
		},
	});
	return { instance, save };
}

/** What stands in for the page until the settings are loaded and manageable. */
function instanceStandIn(instance: {
	isPending: boolean;
	data: InstanceSettings | undefined;
	refetch: () => unknown;
}): ReactNode {
	if (instance.isPending) return <Skeleton className="h-48 rounded-card" />;
	if (!instance.data) {
		return (
			<ErrorState
				frame="raised"
				title="Instance settings did not load"
				body="The control plane did not answer, so nothing was changed."
				retry={() => void instance.refetch()}
			/>
		);
	}
	if (!instance.data.canManage) {
		return (
			<SettingsNotice>
				Only the instance admin, the first account created here, can change
				these settings.
			</SettingsNotice>
		);
	}
	return null;
}

/**
 * Admin → Sign-in and teams: the sign-in providers (single sign-on) and whether
 * strangers may sign up through them, and whether anyone may create a team.
 */
export function SignInSection() {
	const { instance, save } = useInstanceSettings();
	const standIn = instanceStandIn(instance);
	const data = instance.data;
	if (standIn || !data) return standIn;

	const toggleCreation = (next: boolean) =>
		save.mutate(
			{ allowTeamCreation: next },
			{
				onSuccess: () =>
					toast.success(
						next ? "Anyone can create teams" : "Team creation is admin-only",
					),
			},
		);

	return (
		<>
			<div id="sso">
				<ProvidersCard />
			</div>
			<SettingsPlate
				id="team-creation"
				title="Who can create teams"
				tipLabel="About creating teams"
				tip="Every account that creates a team becomes its owner. Turn it off to keep teams to the ones you create or invite people to."
			>
				<div className="flex items-center justify-between gap-4">
					<label
						htmlFor="allow-team-creation"
						className="type-secondary text-ink-2"
					>
						{data.allowTeamCreation
							? "Anyone can create a team."
							: "Only you can create teams. Everyone else joins by invite."}
					</label>
					<Switch
						id="allow-team-creation"
						checked={data.allowTeamCreation}
						disabled={save.isPending}
						onCheckedChange={toggleCreation}
					/>
				</div>
			</SettingsPlate>
		</>
	);
}

/**
 * Admin → Email relay: the SMTP relay that mails invites when a team has no
 * email channel of its own.
 */
export function MailRelaySection() {
	const { instance, save } = useInstanceSettings();
	const data = instance.data;
	const [host, setHost] = useState("");
	const [port, setPort] = useState("587");
	const [tls, setTls] = useState<Tls>("starttls");
	const [username, setUsername] = useState("");
	const [password, setPassword] = useState("");
	const [from, setFrom] = useState("");

	useEffect(() => {
		if (!data) return;
		setHost(data.smtpHost ?? "");
		setPort(String(data.smtpPort ?? 587));
		setTls(data.smtpTls ?? "starttls");
		setUsername(data.smtpUsername ?? "");
		setFrom(data.smtpFrom ?? "");
		setPassword("");
	}, [data]);

	const standIn = instanceStandIn(instance);
	if (standIn || !data) return standIn;

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (save.isPending) return;
		const portNumber = Number.parseInt(port, 10);
		save.mutate(
			{
				smtpHost: host.trim(),
				smtpTls: tls,
				smtpUsername: username.trim(),
				smtpFrom: from.trim(),
				...(Number.isFinite(portNumber) ? { smtpPort: portNumber } : {}),
				...(password !== "" ? { smtpPassword: password } : {}),
			},
			{
				onSuccess: () => {
					setPassword("");
					toast.success("Email relay saved");
				},
			},
		);
	};

	return (
		<SettingsPlate
			id="mail-relay"
			title="Mail relay for invites"
			tipLabel="About the mail relay"
			tip="Used when a team has no email notification channel. Without either, invites are link-only: you copy the link and send it yourself."
		>
			<form onSubmit={submit} className="flex flex-col gap-4">
				<div className="grid gap-3 sm:grid-cols-2">
					<Field>
						<FieldLabel htmlFor="smtp-host">SMTP host</FieldLabel>
						<Input
							id="smtp-host"
							autoComplete="off"
							placeholder="smtp.example.com"
							value={host}
							onChange={(event) => setHost(event.target.value)}
						/>
					</Field>
					<Field>
						<FieldLabel htmlFor="smtp-port">Port</FieldLabel>
						<Input
							id="smtp-port"
							inputMode="numeric"
							value={port}
							onChange={(event) =>
								setPort(event.target.value.replace(/\D/g, ""))
							}
						/>
					</Field>
					<Field>
						<FieldLabel id="smtp-tls-label">Connection security</FieldLabel>
						<Select
							value={tls}
							items={[...TLS_OPTIONS]}
							onValueChange={(next) => {
								const option = TLS_OPTIONS.find((o) => o.value === next);
								if (!option) return;
								setTls(option.value);
								setPort(DEFAULT_PORT[option.value]);
							}}
						>
							<SelectTrigger
								aria-labelledby="smtp-tls-label"
								className="w-full"
							>
								<SelectValue />
							</SelectTrigger>
							<SelectContent alignItemWithTrigger={false} align="start">
								{TLS_OPTIONS.map((option) => (
									<SelectItem key={option.value} value={option.value}>
										{option.label}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</Field>
					<Field>
						<FieldLabel htmlFor="smtp-from">Sender</FieldLabel>
						<Input
							id="smtp-from"
							autoComplete="off"
							placeholder="NebulaCtrl <no-reply@example.com>"
							value={from}
							onChange={(event) => setFrom(event.target.value)}
						/>
					</Field>
					<Field>
						<FieldLabel htmlFor="smtp-username">User name</FieldLabel>
						<Input
							id="smtp-username"
							autoComplete="off"
							value={username}
							onChange={(event) => setUsername(event.target.value)}
						/>
					</Field>
					<Field>
						<FieldLabel htmlFor="smtp-password">Password</FieldLabel>
						<Input
							id="smtp-password"
							type="password"
							autoComplete="new-password"
							placeholder={data.smtpPasswordSet ? "•••••••• (set)" : ""}
							value={password}
							onChange={(event) => setPassword(event.target.value)}
						/>
						<FieldDescription>
							Write-only. Leave empty to keep the stored password.
						</FieldDescription>
					</Field>
				</div>
				{save.isError ? (
					<FieldError>{errorMessage(save.error)}</FieldError>
				) : null}
				<div className="flex items-center gap-3">
					<Button type="submit" variant="secondary" disabled={save.isPending}>
						{save.isPending ? <Spinner /> : null}
						Save mail relay
					</Button>
					<span className="text-ink-3 text-xs">
						{data.smtpConfigured
							? "Invites can be emailed through this relay."
							: "Not configured: invites are link-only."}
					</span>
				</div>
			</form>
		</SettingsPlate>
	);
}
