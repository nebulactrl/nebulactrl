import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { listTokensQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import { createToken } from "#/api/gen/sdk.gen";
import type { CreatedToken } from "#/api/gen/types.gen";
import { meQuery, projectsQuery } from "#/api/hooks";
import { CopyButton } from "#/components/common/copy-button";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
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
import { useCurrentTeam } from "#/features/teams/queries";
import {
	ALL_SCOPES,
	canSkipProductionApproval,
	EXPIRIES,
	type Expiry,
	expiresAtFor,
	type PresetId,
	presetById,
	SCOPE_LABEL,
	type Scope,
	TOKEN_PRESETS,
	type TokenKind,
	tokenKindFor,
} from "./token-presets";

const PRESET_DEFAULT: PresetId = "read-only";

export function CreateTokenDialog({
	open,
	onOpenChange,
	initialPreset = PRESET_DEFAULT,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	initialPreset?: PresetId;
}) {
	const queryClient = useQueryClient();
	const projects = useQuery({ ...projectsQuery(), enabled: open });
	const [name, setName] = useState("");
	const [preset, setPreset] = useState<PresetId>(initialPreset);
	const [custom, setCustom] = useState<ReadonlySet<Scope>>(
		new Set(["projects:read"]),
	);
	const [restricted, setRestricted] = useState<ReadonlySet<string>>(new Set());
	const [expiry, setExpiry] = useState<Expiry["id"]>("90d");
	const [created, setCreated] = useState<CreatedToken | null>(null);
	const [customKind, setCustomKind] = useState<TokenKind>("rest");
	const [skipApproval, setSkipApproval] = useState(false);
	const { role } = useCurrentTeam();
	const me = useQuery({ ...meQuery(), enabled: open });
	const canSkipApproval = canSkipProductionApproval(
		role,
		me.data?.instanceAdmin === true,
	);

	useEffect(() => {
		if (!open) return;
		setName(presetById(initialPreset).name);
		setPreset(initialPreset);
		setCustom(new Set(["projects:read"]));
		setRestricted(new Set());
		setExpiry("90d");
		setCreated(null);
		setCustomKind("rest");
		setSkipApproval(false);
	}, [open, initialPreset]);

	const scopes: readonly string[] =
		preset === "custom"
			? ALL_SCOPES.filter((s) => custom.has(s))
			: presetById(preset).scopes;

	const create = useMutation({
		mutationFn: async () => {
			const days = EXPIRIES.find((e) => e.id === expiry)?.days ?? null;
			const expiresAt = expiresAtFor(days);
			const { data } = await createToken({
				body: {
					name: name.trim(),
					scopes: [...scopes],
					projectIds: restricted.size > 0 ? [...restricted] : null,
					kind: tokenKindFor(preset, customKind),
					...(skipApproval && canSkipApproval
						? { allowProductionWithoutApproval: true }
						: {}),
					...(expiresAt ? { expiresAt } : {}),
				},
			});
			return data;
		},
		onSuccess: async (data) => {
			setCreated(data);
			await queryClient.invalidateQueries({ queryKey: listTokensQueryKey() });
		},
	});

	const choosePreset = (next: PresetId) => {
		setPreset(next);
		// Follow the preset's suggested name until the user has typed their own.
		const suggested = new Set(TOKEN_PRESETS.map((p) => p.name));
		if (name === "" || suggested.has(name)) setName(presetById(next).name);
	};

	const toggle = <T,>(set: ReadonlySet<T>, value: T, on: boolean): Set<T> => {
		const next = new Set(set);
		if (on) next.add(value);
		else next.delete(value);
		return next;
	};

	const valid = name.trim() !== "" && scopes.length > 0;
	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (!valid || create.isPending) return;
		create.mutate();
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="lg">
				{created ? (
					<>
						<DialogHeader>
							<DialogTitle>Copy your token now</DialogTitle>
							<DialogDescription>
								NebulaCtrl keeps only a hash, so a lost token cannot be
								recovered, only replaced.
							</DialogDescription>
						</DialogHeader>
						<DialogBody>
							<div className="flex items-center gap-2">
								<Badge tone="warn" shape="capsule">
									Shown once
								</Badge>
								<span className="text-ink-3 text-xs">
									This is the only time the full token appears.
								</span>
							</div>
							<div className="surface-well flex items-start gap-3 rounded-ctl py-2.5 pr-2.5 pl-3.5">
								<code
									data-testid="created-token"
									className="min-w-0 flex-1 select-all break-all py-1 font-mono text-ink-1 text-xs leading-[1.7]"
								>
									{created.token}
								</code>
								<CopyButton
									variant="chip"
									value={created.token}
									className="shrink-0"
								/>
							</div>
							<p className="text-ink-3 text-xs leading-relaxed">
								Send it as{" "}
								<code className="type-mono text-ink-2">
									Authorization: Bearer …
								</code>
								. Tokens are never accepted in a URL query string.
							</p>
						</DialogBody>
						<DialogFooter>
							<Button variant="primary" onClick={() => onOpenChange(false)}>
								I have copied it
							</Button>
						</DialogFooter>
					</>
				) : (
					<form onSubmit={submit} className="contents">
						<DialogHeader>
							<DialogTitle>New API token</DialogTitle>
							<DialogDescription>
								For scripts, the CLI and MCP clients. It can only do what its
								scopes allow, never more than you.
							</DialogDescription>
						</DialogHeader>
						<DialogBody>
							<Field>
								<FieldLabel htmlFor="token-name">Name</FieldLabel>
								<Input
									id="token-name"
									scale="lg"
									autoFocus
									autoComplete="off"
									value={name}
									onChange={(e) => setName(e.target.value)}
								/>
							</Field>
							{preset === "custom" ? (
								<Field>
									<FieldLabel id="token-kind-label">Use</FieldLabel>
									<div>
										<Segmented
											aria-label="Token use"
											font="sans"
											density="regular"
											value={customKind}
											onValueChange={setCustomKind}
											options={[
												{ value: "rest", label: "REST API and CLI" },
												{ value: "mcp", label: "MCP agent" },
											]}
										/>
									</div>
									<FieldDescription>
										An MCP token works only on /mcp and a REST token only on
										/api/v1; each is refused on the other.
									</FieldDescription>
								</Field>
							) : null}
							<Field>
								<FieldLabel id="token-preset-label">Access</FieldLabel>
								<OptionCardGroup
									value={preset}
									onValueChange={choosePreset}
									aria-labelledby="token-preset-label"
									className="grid grid-cols-2 sm:grid-cols-3"
								>
									{TOKEN_PRESETS.map((p) => (
										<OptionCard
											key={p.id}
											value={p.id}
											title={p.label}
											description={p.summary}
											dashed={p.id === "custom"}
											className="min-w-0"
										/>
									))}
								</OptionCardGroup>
								<FieldDescription>{presetById(preset).hint}</FieldDescription>
							</Field>
							{preset === "custom" ? (
								<fieldset className="surface-well m-0 flex min-w-0 flex-col gap-2 rounded-card p-3">
									<legend className="type-label px-1">Scopes</legend>
									{ALL_SCOPES.map((scope) => (
										<CheckLabel
											key={scope}
											className="items-start gap-2.5 text-sm"
											checkboxClassName="mt-0.5"
											checked={custom.has(scope)}
											onCheckedChange={(on) =>
												setCustom(toggle(custom, scope, on))
											}
										>
											<span className="flex flex-col gap-0.5">
												<span className="type-mono text-ink-1">{scope}</span>
												<span className="text-ink-3 text-xs">
													{SCOPE_LABEL[scope]}
												</span>
											</span>
										</CheckLabel>
									))}
								</fieldset>
							) : (
								<p className="flex flex-wrap gap-1.5">
									{scopes.map((scope) => (
										<code
											key={scope}
											className="surface-well rounded-md px-1.5 py-1 font-mono text-2xs text-ink-2"
										>
											{scope}
										</code>
									))}
								</p>
							)}
							<Field>
								<FieldLabel id="token-projects-label">Projects</FieldLabel>
								<fieldset
									aria-labelledby="token-projects-label"
									className="surface-well m-0 flex max-h-36 min-w-0 flex-col gap-2 overflow-y-auto overscroll-contain rounded-card p-3"
								>
									{projects.data && projects.data.length > 0 ? (
										projects.data.map((project) => (
											<CheckLabel
												key={project.id}
												className="gap-2.5 text-sm"
												checked={restricted.has(project.id)}
												onCheckedChange={(on) =>
													setRestricted(toggle(restricted, project.id, on))
												}
											>
												{project.name}
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
									{restricted.size === 0
										? "No project selected: the token can reach every project."
										: `Restricted to ${restricted.size} ${restricted.size === 1 ? "project" : "projects"}; others answer 404.`}
								</FieldDescription>
							</Field>
							<Field>
								<FieldLabel id="token-expiry-label">Expires</FieldLabel>
								<div>
									<Segmented
										aria-label="Expiry"
										font="sans"
										density="regular"
										value={expiry}
										onValueChange={setExpiry}
										options={EXPIRIES.map((e) => ({
											value: e.id,
											label: e.label,
										}))}
									/>
								</div>
							</Field>
							<Field>
								<FieldLabel id="token-production-label">
									Production changes
								</FieldLabel>
								{canSkipApproval ? (
									<fieldset
										aria-labelledby="token-production-label"
										className="surface-well m-0 flex min-w-0 flex-col gap-2 rounded-card p-3"
									>
										<CheckLabel
											className="items-start gap-2.5 text-sm"
											checkboxClassName="mt-0.5"
											checked={skipApproval}
											onCheckedChange={setSkipApproval}
										>
											<span className="flex flex-col gap-0.5">
												<span className="text-ink-1">
													Skip approval for production changes
												</span>
												<span className="text-ink-3 text-xs">
													Instance admin and team admins only. Off by default.
												</span>
											</span>
										</CheckLabel>
									</fieldset>
								) : null}
								<FieldDescription>
									Changes this token makes to a production environment wait for
									a signed-in person to approve them on the dashboard. This
									applies to every token, MCP or REST, unless an admin switches
									it off here.
								</FieldDescription>
								{skipApproval && canSkipApproval ? (
									<Alert variant="warning">
										<AlertTitle>Production approvals are off</AlertTitle>
										<AlertDescription>
											Whoever holds this token can deploy, restart, stop and
											delete production services without anyone confirming. Use
											it only for a client or pipeline you fully trust. The
											choice is recorded in the audit log.
										</AlertDescription>
									</Alert>
								) : null}
							</Field>
							{create.isError ? (
								<FieldError>{errorMessage(create.error)}</FieldError>
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
								variant={valid ? "primary" : "secondary"}
								disabled={!valid || create.isPending}
							>
								{create.isPending ? <Spinner /> : null}
								Create token
							</Button>
						</DialogFooter>
					</form>
				)}
			</DialogContent>
		</Dialog>
	);
}
