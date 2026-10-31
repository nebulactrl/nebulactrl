import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
	type FormEvent,
	type ReactNode,
	useEffect,
	useRef,
	useState,
} from "react";
import { errorMessage } from "#/api/client-config";
import { listNotificationEventTypesOptions } from "#/api/gen/@tanstack/react-query.gen";
import {
	createNotificationChannel,
	updateNotificationChannel,
} from "#/api/gen/sdk.gen";
import type { ChannelView } from "#/api/gen/types.gen";
import { projectsQuery } from "#/api/hooks";
import { CredentialHelp } from "#/components/common/credential-help";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
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
import { OptionCard, OptionCardGroup } from "#/components/ui/option-card";
import { Segmented } from "#/components/ui/segmented";
import { Spinner } from "#/components/ui/spinner";
import { Textarea } from "#/components/ui/textarea";
import { toast } from "#/components/ui/toast";
import {
	discordWebhookGuide,
	slackWebhookGuide,
	smtpGuide,
	telegramGuide,
} from "#/lib/credential-links";
import { ChannelIcon } from "./channel-icon";
import { EventMatrix } from "./event-matrix";
import {
	CHANNEL_KINDS,
	type ChannelKind,
	createBody,
	type Draft,
	defaultPort,
	draftFor,
	draftProblem,
	hintHost,
	kindLabel,
	patchBody,
	presetTypes,
	type TlsMode,
} from "./model";
import { invalidateNotifications } from "./queries";
import { SigningSecretPanel } from "./signing-secret";

type Scope = "all" | "projects";

function SecretInput({
	id,
	value,
	onChange,
	stored,
	placeholder,
	label,
	description,
	mono = true,
}: {
	id: string;
	value: string;
	onChange: (next: string) => void;
	/** A hint of the stored secret when editing (empty field keeps it). */
	stored: string | undefined;
	placeholder?: string;
	label: string;
	description?: ReactNode;
	mono?: boolean;
}) {
	return (
		<Field>
			<FieldLabel htmlFor={id}>
				{label}
				{stored ? " (leave empty to keep)" : ""}
			</FieldLabel>
			<Input
				id={id}
				type="password"
				autoComplete="new-password"
				spellCheck={false}
				className={mono ? "font-mono" : undefined}
				placeholder={stored ? `${stored}` : placeholder}
				value={value}
				onChange={(e) => onChange(e.target.value)}
			/>
			{description ? <FieldDescription>{description}</FieldDescription> : null}
		</Field>
	);
}

/**
 * Create or edit a notification channel. Credentials are write-only: an edit
 * shows a masked hint and an empty field keeps the stored value. A webhook's
 * generated signing secret is shown once, right after creation.
 */
export function ChannelDialog({
	open,
	onOpenChange,
	channel,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	channel: ChannelView | null;
}) {
	const queryClient = useQueryClient();
	const editing = channel !== null;
	const eventTypes = useQuery({
		...listNotificationEventTypesOptions(),
		enabled: open,
	});
	const projects = useQuery({ ...projectsQuery(), enabled: open });
	const types = eventTypes.data?.items ?? [];

	const [draft, setDraft] = useState<Draft>(() => draftFor(channel, []));
	const [error, setError] = useState<string | null>(null);
	const [secret, setSecret] = useState<{ name: string; value: string } | null>(
		null,
	);
	// A new channel starts on the defaults once the catalogue has loaded, unless
	// the user already chose events.
	const eventsTouched = useRef(false);

	useEffect(() => {
		if (!open) return;
		eventsTouched.current = false;
		setDraft(draftFor(channel, []));
		setError(null);
		setSecret(null);
	}, [open, channel]);

	useEffect(() => {
		if (open && !editing && !eventsTouched.current && types.length > 0) {
			setDraft((d) => ({ ...d, eventTypes: presetTypes("alerts", types) }));
		}
	}, [open, editing, types]);

	const set = <K extends keyof Draft>(key: K, value: Draft[K]) =>
		setDraft((prev) => ({ ...prev, [key]: value }));

	const save = useMutation({
		mutationFn: async () => {
			if (channel) {
				await updateNotificationChannel({
					path: { channelId: channel.id },
					body: patchBody(draft),
				});
				return null;
			}
			const { data } = await createNotificationChannel({
				body: createBody(draft),
			});
			return data;
		},
		onSuccess: async (created) => {
			await invalidateNotifications(queryClient);
			if (created?.signingSecret) {
				toast.success("Channel added", "Copy the signing secret now.");
				setSecret({ name: created.channel.name, value: created.signingSecret });
				return;
			}
			toast.success(
				editing ? "Channel saved" : "Channel added",
				"Send a test to confirm it arrives.",
			);
			onOpenChange(false);
		},
		onError: (e) => setError(errorMessage(e)),
	});

	const problem = draftProblem(draft, editing);
	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (problem || save.isPending) return;
		setError(null);
		save.mutate();
	};

	const toggleProject = (id: string, on: boolean) =>
		set(
			"projectIds",
			on ? [...draft.projectIds, id] : draft.projectIds.filter((p) => p !== id),
		);

	if (secret) {
		return (
			<Dialog open={open} onOpenChange={onOpenChange}>
				<DialogContent size="lg">
					<DialogHeader>
						<DialogTitle>Copy the signing secret for {secret.name}</DialogTitle>
						<DialogDescription>
							Your receiver uses it to check that a request really came from
							NebulaCtrl.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						<SigningSecretPanel secret={secret.value} />
					</DialogBody>
					<DialogFooter>
						<Button variant="primary" onClick={() => onOpenChange(false)}>
							I have copied it
						</Button>
					</DialogFooter>
				</DialogContent>
			</Dialog>
		);
	}

	const kind = draft.kind;
	const stored = channel?.secrets ?? {};
	const scope: Scope = draft.allProjects ? "all" : "projects";

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="lg">
				<form onSubmit={submit} className="contents">
					<DialogHeader>
						<DialogTitle>
							{editing ? `Edit ${channel.name}` : "Add notification channel"}
						</DialogTitle>
						<DialogDescription>
							Where NebulaCtrl tells you about failed deployments, offline
							servers, backups and more. Credentials are stored encrypted and
							never shown again.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						{editing ? null : (
							<Field>
								<FieldLabel id="channel-kind-label">Type</FieldLabel>
								<OptionCardGroup<ChannelKind>
									value={kind}
									onValueChange={(next) => set("kind", next)}
									aria-labelledby="channel-kind-label"
									className="grid grid-cols-2 sm:grid-cols-5"
								>
									{CHANNEL_KINDS.map((k) => (
										<OptionCard
											key={k.kind}
											value={k.kind}
											title={
												<span className="flex flex-col items-start gap-2">
													<ChannelIcon kind={k.kind} size={24} />
													{k.label}
												</span>
											}
											description={k.blurb}
											className="min-w-0"
										/>
									))}
								</OptionCardGroup>
							</Field>
						)}
						<Field>
							<FieldLabel htmlFor="channel-name">Name</FieldLabel>
							<Input
								id="channel-name"
								autoFocus
								autoComplete="off"
								placeholder={
									kind === "email" ? "Ops mailbox" : `${kindLabel(kind)} alerts`
								}
								value={draft.name}
								onChange={(e) => set("name", e.target.value)}
							/>
						</Field>

						{kind === "slack" || kind === "discord" ? (
							<>
								<CredentialHelp
									guide={
										kind === "slack"
											? slackWebhookGuide()
											: discordWebhookGuide()
									}
									fieldIds={["channel-url"]}
								/>
								<SecretInput
									id="channel-url"
									label="Webhook URL"
									value={draft.url}
									onChange={(v) => set("url", v)}
									stored={stored.url}
									placeholder={
										kind === "slack"
											? "https://hooks.slack.com/services/T…/B…/…"
											: "https://discord.com/api/webhooks/…"
									}
									description="The URL is the credential."
								/>
							</>
						) : null}

						{kind === "telegram" ? (
							<>
								<CredentialHelp
									guide={telegramGuide()}
									fieldIds={["channel-bot-token", "channel-chat-id"]}
								/>
								<SecretInput
									id="channel-bot-token"
									label="Bot token"
									value={draft.botToken}
									onChange={(v) => set("botToken", v)}
									stored={stored.botToken}
									placeholder="123456:ABC-DEF…"
								/>
								<Field>
									<FieldLabel htmlFor="channel-chat-id">Chat</FieldLabel>
									<Input
										id="channel-chat-id"
										autoComplete="off"
										spellCheck={false}
										className="font-mono"
										placeholder="-1001234567890 or @channelname"
										value={draft.chatId}
										onChange={(e) => set("chatId", e.target.value)}
									/>
									<FieldDescription>
										The chat's numeric id, or its @name for a public channel.
									</FieldDescription>
								</Field>
							</>
						) : null}

						{kind === "email" ? (
							<>
								<div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_120px]">
									<Field>
										<FieldLabel htmlFor="channel-host">SMTP server</FieldLabel>
										<Input
											id="channel-host"
											autoComplete="off"
											spellCheck={false}
											placeholder="smtp.example.com"
											value={draft.host}
											onChange={(e) => set("host", e.target.value)}
										/>
									</Field>
									<Field>
										<FieldLabel htmlFor="channel-port">Port</FieldLabel>
										<Input
											id="channel-port"
											inputMode="numeric"
											autoComplete="off"
											className="font-mono"
											placeholder={String(defaultPort(draft.tls))}
											value={draft.port}
											onChange={(e) => set("port", e.target.value)}
										/>
									</Field>
								</div>
								<Field>
									<FieldLabel id="channel-tls-label">Connection</FieldLabel>
									<div>
										<Segmented<TlsMode>
											aria-label="Connection security"
											font="sans"
											density="regular"
											value={draft.tls}
											onValueChange={(v) => set("tls", v)}
											options={[
												{ value: "starttls", label: "STARTTLS" },
												{ value: "implicit", label: "TLS" },
												{ value: "none", label: "None" },
											]}
										/>
									</div>
									<FieldDescription>
										STARTTLS (port 587) upgrades the connection; TLS (port 465)
										is encrypted from the first byte.
									</FieldDescription>
								</Field>
								{draft.tls === "none" ? (
									<Alert variant="warning">
										<AlertTitle>Not encrypted</AlertTitle>
										<AlertDescription>
											The message, and the password if you set one, travel in
											clear text. Use this only for a relay on a trusted
											network.
										</AlertDescription>
									</Alert>
								) : null}
								<CredentialHelp
									guide={smtpGuide()}
									fieldIds={["channel-username", "channel-password"]}
								/>
								<div className="grid gap-3 sm:grid-cols-2">
									<Field>
										<FieldLabel htmlFor="channel-username">
											User name
										</FieldLabel>
										<Input
											id="channel-username"
											autoComplete="off"
											spellCheck={false}
											placeholder="empty: no authentication"
											value={draft.username}
											onChange={(e) => set("username", e.target.value)}
										/>
									</Field>
									<SecretInput
										id="channel-password"
										label="Password"
										mono={false}
										value={draft.password}
										onChange={(v) => set("password", v)}
										stored={stored.password}
									/>
								</div>
								<Field>
									<FieldLabel htmlFor="channel-from">From</FieldLabel>
									<Input
										id="channel-from"
										autoComplete="off"
										spellCheck={false}
										placeholder="NebulaCtrl <alerts@example.com>"
										value={draft.from}
										onChange={(e) => set("from", e.target.value)}
									/>
								</Field>
								<Field>
									<FieldLabel htmlFor="channel-to">Recipients</FieldLabel>
									<Textarea
										id="channel-to"
										rows={2}
										spellCheck={false}
										placeholder={"ops@example.com\noncall@example.com"}
										value={draft.to}
										onChange={(e) => set("to", e.target.value)}
									/>
									<FieldDescription>
										One address per line, or separated by commas.
									</FieldDescription>
								</Field>
								<CheckLabel
									checked={draft.insecureSkipVerify}
									onCheckedChange={(v) => set("insecureSkipVerify", v)}
								>
									Accept a self-signed certificate (the connection stays
									encrypted, but the server's identity is not checked)
								</CheckLabel>
							</>
						) : null}

						{kind === "webhook" ? (
							<>
								<SecretInput
									id="channel-webhook-url"
									label="URL"
									value={draft.url}
									onChange={(v) => set("url", v)}
									stored={stored.url}
									placeholder="https://example.com/hooks/nebulactrl"
									description="NebulaCtrl POSTs a JSON body to this URL and signs it with HMAC-SHA256 in the X-NebulaCtrl-Signature header. LAN and localhost targets are fine; cloud metadata addresses are refused."
								/>
								{editing ? null : (
									<SecretInput
										id="channel-signing-secret"
										label="Signing secret"
										value={draft.signingSecret}
										onChange={(v) => set("signingSecret", v)}
										stored={undefined}
										placeholder="empty: generate one"
										description="Leave empty and NebulaCtrl generates one and shows it once after you save."
									/>
								)}
								{editing && stored.url ? (
									<FieldDescription>
										Currently sending to{" "}
										{hintHost(stored.url) ?? "a stored URL"}. The signing secret
										is changed with “Rotate secret” on the card.
									</FieldDescription>
								) : null}
							</>
						) : null}

						<EventMatrix
							types={types}
							selected={draft.eventTypes}
							onSelectedChange={(next) => {
								eventsTouched.current = true;
								set("eventTypes", next);
							}}
							minSeverity={draft.minSeverity}
							onMinSeverityChange={(next) => set("minSeverity", next)}
						/>

						<Field>
							<FieldLabel id="channel-scope-label">Projects</FieldLabel>
							<div>
								<Segmented<Scope>
									aria-label="Which projects"
									font="sans"
									density="regular"
									value={scope}
									onValueChange={(next) => set("allProjects", next === "all")}
									options={[
										{ value: "all", label: "Whole team" },
										{ value: "projects", label: "Selected projects" },
									]}
								/>
							</div>
							{draft.allProjects ? (
								<FieldDescription>
									Every project, plus events that belong to no project, such as
									server alerts.
								</FieldDescription>
							) : (
								<>
									<fieldset
										aria-labelledby="channel-scope-label"
										className="surface-well m-0 flex max-h-36 min-w-0 flex-col gap-2 overflow-y-auto overscroll-contain rounded-card p-3"
									>
										{projects.data && projects.data.length > 0 ? (
											projects.data.map((p) => (
												<CheckLabel
													key={p.id}
													className="gap-2.5 text-sm"
													checked={draft.projectIds.includes(p.id)}
													onCheckedChange={(on) => toggleProject(p.id, on)}
												>
													{p.name}
												</CheckLabel>
											))
										) : (
											<span className="text-ink-3 text-xs">
												{projects.isPending
													? "Loading projects…"
													: "No projects yet."}
											</span>
										)}
									</fieldset>
									<FieldDescription>
										Server events (offline, disk, certificates) belong to no
										project, so only whole-team channels receive them.
									</FieldDescription>
								</>
							)}
						</Field>

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
						<Button
							type="submit"
							variant={problem ? "secondary" : "primary"}
							disabled={problem !== null || save.isPending}
							title={problem ?? undefined}
						>
							{save.isPending ? <Spinner /> : null}
							{editing ? "Save" : "Add channel"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
