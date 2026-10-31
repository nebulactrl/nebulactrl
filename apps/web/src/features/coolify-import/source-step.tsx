import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useId } from "react";
import { listGitConnectionsOptions } from "#/api/gen/@tanstack/react-query.gen";
import type { Server } from "#/api/models";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Field, FieldDescription, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { ServerPicker } from "#/features/create/create-dialog";
import { usableConnections } from "#/features/git/repo-picker";
import type { SourceDraft } from "./lib";

/** The form's id, so the dialog footer's "Read Coolify" key submits it. */
export const SOURCE_FORM_ID = "coolify-import-source";

const NO_CONNECTION = "none";

/** What a failed read says, and which field it points at. */
export interface SourceProblem {
	message: string;
	code: string | undefined;
}

/**
 * Step one: which Coolify to read and which server takes what is imported.
 * The token is typed into a password field whose value is the dialog's own
 * state; nothing here writes it anywhere else.
 */
export function SourceStep({
	source,
	onSource,
	servers,
	serverId,
	onServer,
	gitConnectionId,
	onGitConnection,
	problem,
	onSubmit,
}: {
	source: SourceDraft;
	onSource(next: SourceDraft): void;
	servers: readonly Server[];
	serverId: string;
	onServer(id: string): void;
	gitConnectionId: string;
	onGitConnection(id: string): void;
	problem: SourceProblem | null;
	onSubmit(): void;
}) {
	const id = useId();
	const connections = useQuery(listGitConnectionsOptions());
	const usable = usableConnections(connections.data?.items ?? []);
	const urlInvalid = problem?.code === "invalid_coolify_url";
	const tokenInvalid = problem?.code === "coolify_unauthorized";
	const chosen = servers.find((server) => server.id === serverId);

	return (
		<form
			id={SOURCE_FORM_ID}
			autoComplete="off"
			className="flex flex-col gap-6"
			onSubmit={(event) => {
				event.preventDefault();
				onSubmit();
			}}
		>
			<div className="flex flex-col gap-4">
				<Field>
					<FieldLabel htmlFor={`${id}-url`}>Coolify URL</FieldLabel>
					<Input
						id={`${id}-url`}
						autoFocus
						type="text"
						inputMode="url"
						autoComplete="off"
						autoCapitalize="off"
						spellCheck={false}
						placeholder="https://coolify.example.com"
						className="font-mono"
						aria-invalid={urlInvalid}
						value={source.url}
						onChange={(event) =>
							onSource({ ...source, url: event.target.value })
						}
					/>
					<FieldDescription>
						The address you open Coolify at. The control plane connects to it
						from its own host.
					</FieldDescription>
				</Field>
				<Field>
					<FieldLabel htmlFor={`${id}-token`}>API token</FieldLabel>
					<Input
						id={`${id}-token`}
						type="password"
						autoComplete="off"
						autoCapitalize="off"
						spellCheck={false}
						placeholder="1|…"
						className="font-mono"
						aria-invalid={tokenInvalid}
						aria-describedby={`${id}-token-help`}
						value={source.token}
						onChange={(event) =>
							onSource({ ...source, token: event.target.value })
						}
					/>
					<FieldDescription id={`${id}-token-help`}>
						Create it in Coolify under Keys &amp; Tokens, API tokens. It needs
						the <span className="font-mono text-ink-2">read</span> and{" "}
						<span className="font-mono text-ink-2">read:sensitive</span>{" "}
						abilities. The token is sent for this request only and never stored.
					</FieldDescription>
				</Field>
			</div>

			<section aria-label="Server" className="flex flex-col gap-2">
				<h3 className="type-label m-0">Server</h3>
				{servers.length === 0 ? (
					<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
						No server is enrolled yet.{" "}
						<Link
							to="/servers"
							className="text-accent-ink underline-offset-2 hover:underline"
						>
							Add a server
						</Link>{" "}
						first.
					</p>
				) : (
					<>
						<ServerPicker
							servers={servers}
							value={serverId}
							onChange={onServer}
						/>
						<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
							The imported services run here.{" "}
							{chosen && !chosen.unrestricted
								? "This server is restricted: a service that needs privileged mode, host devices or another host-access option is flagged in the review."
								: chosen?.unrestricted
									? "This server is unrestricted, so host-access options are accepted."
									: ""}
						</p>
					</>
				)}
			</section>

			{usable.length > 0 ? (
				<Field>
					<FieldLabel htmlFor={`${id}-git`}>
						Git connection (optional)
					</FieldLabel>
					<Select
						value={gitConnectionId === "" ? NO_CONNECTION : gitConnectionId}
						items={[
							{ value: NO_CONNECTION, label: "None" },
							...usable.map((c) => ({ value: c.id, label: c.name })),
						]}
						onValueChange={(next) => {
							const value = String(next ?? NO_CONNECTION);
							onGitConnection(value === NO_CONNECTION ? "" : value);
						}}
					>
						<SelectTrigger id={`${id}-git`} className="w-full max-w-sm">
							<SelectValue />
						</SelectTrigger>
						<SelectContent alignItemWithTrigger={false}>
							<SelectItem value={NO_CONNECTION}>None</SelectItem>
							{usable.map((c) => (
								<SelectItem key={c.id} value={c.id}>
									{c.name}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
					<FieldDescription>
						Links the git applications to a connected account, so builds can
						clone private repositories. Without it they clone over a public URL.
					</FieldDescription>
				</Field>
			) : null}

			{problem ? (
				<Alert variant="destructive">
					<AlertTitle>Coolify could not be read</AlertTitle>
					<AlertDescription>
						<p>{problem.message} Nothing was changed.</p>
					</AlertDescription>
				</Alert>
			) : null}
		</form>
	);
}
