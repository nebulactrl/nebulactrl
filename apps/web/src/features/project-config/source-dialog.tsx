import { useQuery } from "@tanstack/react-query";
import { useEffect, useId, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { listGitConnectionsOptions } from "#/api/gen/@tanstack/react-query.gen";
import type { Environment } from "#/api/models";
import { DocsButton } from "#/components/common/docs-button";
import { Relative } from "#/components/common/relative";
import { Badge } from "#/components/ui/badge";
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
import { Field, FieldError, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Spinner } from "#/components/ui/spinner";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import {
	Tooltip,
	TooltipContent,
	TooltipTrigger,
} from "#/components/ui/tooltip";
import { autoApplySwitch, modeWords, shortSha, statusWords } from "./model";
import {
	useCheckBinding,
	useClearBinding,
	useEnvironmentConfigSource,
	useUpdateBinding,
} from "./queries";

/**
 * Environment settings → "Config from git": which file the environment reads
 * (explicit, or derived from its git services), the last check, Check now,
 * and the Auto-apply switch. Auto-apply is never available in production and
 * needs the admin scope; the control plane says so when it refuses.
 */
export function SourceDialog({
	environment,
	open,
	onOpenChange,
}: {
	environment: Environment;
	open: boolean;
	onOpenChange(open: boolean): void;
}) {
	const ids = useId();
	const source = useEnvironmentConfigSource(environment.id, open);
	const connections = useQuery({
		...listGitConnectionsOptions(),
		enabled: open,
	});
	const update = useUpdateBinding(environment.id);
	const clear = useClearBinding(environment.id);
	const check = useCheckBinding(environment.id);

	const [connectionId, setConnectionId] = useState("");
	const [repo, setRepo] = useState("");
	const [branch, setBranch] = useState("");
	const [path, setPath] = useState("");
	const data = source.data;
	// The form starts from what is stored, once per opening.
	// biome-ignore lint/correctness/useExhaustiveDependencies: seeded when the answer first arrives.
	useEffect(() => {
		if (!open || data === undefined) return;
		setConnectionId(data.connectionId ?? "");
		setRepo(data.repo ?? "");
		setBranch(data.branch ?? "");
		setPath(data.mode === "explicit" ? (data.path ?? "") : "");
	}, [open, data === undefined]);

	const bound = data !== undefined && data.mode !== "none";
	const auto = autoApplySwitch({
		isProduction: environment.isProduction,
		bound,
	});
	const busy = update.isPending || clear.isPending || check.isPending;
	const error = update.error ?? clear.error ?? check.error;
	const complete =
		connectionId !== "" && repo.trim() !== "" && branch.trim() !== "";
	const items = (connections.data?.items ?? []).map((connection) => ({
		value: connection.id,
		label: connection.name,
	}));

	const save = () =>
		update.mutate(
			{
				connectionId,
				repo: repo.trim(),
				branch: branch.trim(),
				...(path.trim() !== "" ? { path: path.trim() } : {}),
			},
			{
				onSuccess: () =>
					toast.success("Saved", "nebula.toml is read from this repository."),
			},
		);

	const switchControl = (
		<Switch
			id={`${ids}-auto`}
			checked={data?.autoApply ?? false}
			disabled={auto.disabled || busy}
			aria-describedby={`${ids}-auto-note`}
			onCheckedChange={(next) =>
				update.mutate(
					{ autoApply: next },
					{
						onSuccess: () =>
							toast.success(
								next ? "Auto-apply on" : "Auto-apply off",
								next
									? "Pushes stage the file's changes. Nothing is deployed."
									: "Pushes only mark the environment as differing.",
							),
					},
				)
			}
		/>
	);

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="md" showCloseButton>
				<DialogHeader>
					<DialogTitle>Config from git</DialogTitle>
					<DialogDescription>
						Read nebula.toml from a repository on every push to its branch, and
						show here when it differs from {environment.name}.
					</DialogDescription>
				</DialogHeader>
				<DialogBody className="flex flex-col gap-4">
					{data ? (
						<dl className="surface-well grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 rounded-ctl px-3 py-2.5 text-xs">
							<dt className="text-ink-3">Linked</dt>
							<dd className="text-ink-1">{modeWords(data.mode)}</dd>
							{bound ? (
								<>
									<dt className="text-ink-3">File</dt>
									<dd className="min-w-0 truncate font-mono text-ink-1">
										{data.repo}@{data.branch} ·{" "}
										{data.resolvedPath || data.path || "nebula.toml"}
									</dd>
									<dt className="text-ink-3">Last check</dt>
									<dd className="flex min-w-0 flex-wrap items-center gap-2 text-ink-1">
										<Badge
											variant={
												data.status === "invalid" ? "destructive" : "default"
											}
										>
											{statusWords(data.status)}
										</Badge>
										{data.commit ? (
											<span className="font-mono text-ink-3">
												{shortSha(data.commit)}
											</span>
										) : null}
										<span className="text-ink-3">
											<Relative iso={data.checkedAt} />
										</span>
									</dd>
									{data.statusDetail && data.status !== "in_sync" ? (
										<>
											<dt className="text-ink-3">Detail</dt>
											<dd className="break-words text-ink-2">
												{data.statusDetail}
											</dd>
										</>
									) : null}
								</>
							) : null}
						</dl>
					) : null}
					<div className="flex flex-col gap-3">
						<Field>
							<FieldLabel htmlFor={`${ids}-connection`}>
								Git connection
							</FieldLabel>
							<Select
								value={connectionId === "" ? null : connectionId}
								items={items}
								onValueChange={(next) => setConnectionId(String(next ?? ""))}
							>
								<SelectTrigger id={`${ids}-connection`} className="w-full">
									<SelectValue placeholder="Pick a connection" />
								</SelectTrigger>
								<SelectContent alignItemWithTrigger={false}>
									{items.map((item) => (
										<SelectItem key={item.value} value={item.value}>
											{item.label}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</Field>
						<div className="grid grid-cols-2 gap-3">
							<Field>
								<FieldLabel htmlFor={`${ids}-repo`}>Repository</FieldLabel>
								<Input
									id={`${ids}-repo`}
									autoComplete="off"
									spellCheck={false}
									placeholder="acme/shop"
									value={repo}
									onChange={(event) => setRepo(event.target.value)}
								/>
							</Field>
							<Field>
								<FieldLabel htmlFor={`${ids}-branch`}>Branch</FieldLabel>
								<Input
									id={`${ids}-branch`}
									autoComplete="off"
									spellCheck={false}
									placeholder="main"
									value={branch}
									onChange={(event) => setBranch(event.target.value)}
								/>
							</Field>
						</div>
						<Field>
							<FieldLabel htmlFor={`${ids}-path`}>
								Path <span className="font-normal text-ink-4">(optional)</span>
							</FieldLabel>
							<Input
								id={`${ids}-path`}
								autoComplete="off"
								spellCheck={false}
								placeholder="nebula.toml"
								value={path}
								onChange={(event) => setPath(event.target.value)}
							/>
						</Field>
					</div>
					<div className="flex items-start justify-between gap-4 border-line-faint border-t pt-3">
						<div className="flex min-w-0 flex-col gap-1">
							<label
								htmlFor={`${ids}-auto`}
								className="font-medium text-ink-1 text-sm"
							>
								Auto-apply
							</label>
							<span id={`${ids}-auto-note`} className="type-secondary">
								{auto.reason ??
									"On each push, stage the file's changes. Nothing is deployed. Needs the admin scope; it is never available in production."}
							</span>
						</div>
						{auto.disabled ? (
							<Tooltip>
								<TooltipTrigger render={<span className="inline-flex" />}>
									{switchControl}
								</TooltipTrigger>
								<TooltipContent>{auto.reason}</TooltipContent>
							</Tooltip>
						) : (
							switchControl
						)}
					</div>
					{error ? <FieldError>{errorMessage(error)}</FieldError> : null}
				</DialogBody>
				<DialogFooter className="items-center">
					<DocsButton path="operations/project-config.md">
						How nebula.toml works
					</DocsButton>
					<div className="ml-auto flex items-center gap-2">
						{data?.mode === "explicit" ? (
							<Button
								variant="ghost"
								disabled={busy}
								onClick={() =>
									clear.mutate(undefined, {
										onSuccess: () => toast.success("Link removed"),
									})
								}
							>
								Remove link
							</Button>
						) : null}
						<Button
							variant="secondary"
							disabled={busy || !bound}
							onClick={() =>
								check.mutate(undefined, {
									onSuccess: (checked) =>
										toast.success(
											"Checked nebula.toml",
											statusWords(checked.status),
										),
								})
							}
						>
							{check.isPending ? <Spinner /> : null}
							Check now
						</Button>
						<Button
							variant="primary"
							disabled={busy || !complete}
							onClick={save}
						>
							{update.isPending ? <Spinner /> : null}
							Save
						</Button>
					</div>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
