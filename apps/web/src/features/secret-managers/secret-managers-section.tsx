import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { PlusIcon } from "lucide-react";
import { type FormEvent, useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { deleteSecretManager, testSecretManager } from "#/api/gen/sdk.gen";
import type {
	SecretManager,
	SecretManagerTestResult,
} from "#/api/gen/types.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { CopyButton } from "#/components/common/copy-button";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
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
import { Field, FieldDescription, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { StatusIcon } from "#/components/ui/status-icon";
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "#/components/ui/table";
import { toast } from "#/components/ui/toast";
import { referenceHelp } from "#/lib/secret-manager-guides";
import { KIND_LABEL, scopeSummary, whereOf } from "./model";
import { invalidateSecretManagers, secretManagersQuery } from "./queries";
import { SecretManagerDialog } from "./secret-manager-dialog";

function TestDialog({
	manager,
	onClose,
}: {
	manager: SecretManager | null;
	onClose: () => void;
}) {
	const queryClient = useQueryClient();
	const [path, setPath] = useState("");
	const [key, setKey] = useState("");
	const [result, setResult] = useState<SecretManagerTestResult | null>(null);

	useEffect(() => {
		if (manager) {
			setPath("");
			setKey("");
			setResult(null);
		}
	}, [manager]);

	const run = useMutation({
		mutationFn: async () => {
			if (!manager) return null;
			const { data } = await testSecretManager({
				path: { id: manager.id },
				body: { path: path.trim(), key: key.trim() },
			});
			return data ?? null;
		},
		onSuccess: async (data) => {
			setResult(data);
			await invalidateSecretManagers(queryClient);
		},
		onError: (e) =>
			toast.error("Could not test the secret manager", errorMessage(e)),
	});

	// Doppler's path is optional (defaults or a service token fix it); the others need both.
	const doppler = manager?.kind === "doppler";
	const half = doppler
		? path.trim() !== "" && key.trim() === ""
		: (path.trim() === "") !== (key.trim() === "");
	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (half || run.isPending) return;
		run.mutate();
	};

	return (
		<Dialog
			open={manager !== null}
			onOpenChange={(open) => (open ? null : onClose())}
		>
			<DialogContent size="md">
				<form onSubmit={submit} className="contents">
					<DialogHeader>
						<DialogTitle>Test {manager?.name}</DialogTitle>
						<DialogDescription>
							NebulaCtrl signs in and, if you name a secret, reads it to check
							the path and the permissions. The value is thrown away and never
							shown.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						<Field>
							<FieldLabel htmlFor="sm-test-path">
								{doppler ? "Project and config (optional)" : "Secret path"}
							</FieldLabel>
							<Input
								id="sm-test-path"
								autoComplete="off"
								spellCheck={false}
								placeholder={
									manager?.kind === "infisical"
										? "prod/backend"
										: doppler
											? "backend/prd"
											: "apps/shop/db"
								}
								value={path}
								onChange={(e) => setPath(e.target.value)}
							/>
						</Field>
						<Field>
							<FieldLabel htmlFor="sm-test-key">
								{manager?.kind === "vault" ? "Field" : "Secret name"}
							</FieldLabel>
							<Input
								id="sm-test-key"
								autoComplete="off"
								spellCheck={false}
								placeholder={
									manager?.kind === "vault" ? "password" : "DATABASE_URL"
								}
								value={key}
								onChange={(e) => setKey(e.target.value)}
							/>
							<FieldDescription>
								{manager ? referenceHelp(manager.kind) : ""} Leave both empty to
								only test the sign-in.
							</FieldDescription>
						</Field>
						{result ? (
							<Alert variant={result.ok ? "success" : "destructive"}>
								<AlertTitle>{result.ok ? "It works" : "It failed"}</AlertTitle>
								<AlertDescription>{result.message}</AlertDescription>
							</Alert>
						) : null}
					</DialogBody>
					<DialogFooter>
						<Button type="button" variant="ghost" onClick={onClose}>
							Close
						</Button>
						<Button
							type="submit"
							variant="primary"
							disabled={half || run.isPending}
						>
							{run.isPending ? <Spinner /> : null}
							{run.isPending ? "Testing…" : "Run test"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}

function TestState({ manager }: { manager: SecretManager }) {
	const last = manager.lastTest;
	if (last.ok === null) {
		return (
			<span className="flex items-center gap-2 text-ink-3 text-xs">
				<StatusIcon kind="pending" label="" /> Not tested
			</span>
		);
	}
	return (
		<span className="flex flex-col gap-0.5">
			<span className="flex items-center gap-2 text-ink-2 text-xs">
				<StatusIcon kind={last.ok ? "healthy" : "failed"} label="" />
				{last.ok ? "Works" : "Failed"}
				{last.at ? (
					<span className="text-ink-4">
						· <Relative iso={last.at} />
					</span>
				) : null}
			</span>
			{last.ok ? null : (
				<span
					className="max-w-[320px] truncate text-destructive-text text-xs"
					title={last.error}
				>
					{last.error}
				</span>
			)}
		</span>
	);
}

/**
 * Team settings → Integrations → Secret managers: Vault / OpenBao and Infisical.
 * A variable reads a secret from one as `${{vault:<name>/<path>#<key>}}`; the
 * value is fetched when a deploy starts and never stored.
 */
export function SecretManagersSection() {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const managers = useQuery(secretManagersQuery());
	const [editing, setEditing] = useState<SecretManager | null>(null);
	const [creating, setCreating] = useState(false);
	const [testing, setTesting] = useState<SecretManager | null>(null);

	const remove = useMutation({
		mutationFn: async (m: SecretManager) => {
			await deleteSecretManager({ path: { id: m.id } });
			return m;
		},
		onSuccess: async (m) => {
			toast.success(`Removed ${m.name}`);
			await invalidateSecretManagers(queryClient);
		},
		onError: (e) =>
			toast.error("Could not remove the secret manager", errorMessage(e)),
	});

	if (managers.isPending) return <Skeleton className="h-32 rounded-card" />;
	if (managers.isError && !managers.data) {
		return (
			<ErrorState
				frame="raised"
				title="Secret managers did not load"
				body="The control plane did not answer, so nothing was changed."
				retry={() => void managers.refetch()}
			/>
		);
	}
	const items = managers.data ?? [];
	// Connect a provider (above it) is the page's one primary action.
	const add = (
		<Button variant="secondary" onClick={() => setCreating(true)}>
			<PlusIcon />
			Add secret manager
		</Button>
	);

	return (
		<div className="flex flex-col gap-4">
			{items.length === 0 ? (
				<EmptyState
					illustration="key"
					title="Keep secrets where they already live"
					body="Connect HashiCorp Vault, OpenBao, Infisical or Doppler. A variable then points at a secret instead of holding it, and NebulaCtrl reads it when a deploy starts."
					action={add}
				/>
			) : (
				<>
					<div className="flex justify-end">{add}</div>
					<div className="overflow-hidden border-line border-y">
						<Table stack>
							<TableHeader>
								<TableRow>
									<TableHead>Name</TableHead>
									<TableHead>Where</TableHead>
									<TableHead>Reference</TableHead>
									<TableHead>Available to</TableHead>
									<TableHead>Test</TableHead>
									<TableHead className="w-px" />
								</TableRow>
							</TableHeader>
							<TableBody>
								{items.map((m) => (
									<TableRow key={m.id}>
										<TableCell pin="start">
											<span className="flex flex-col">
												<span className="font-medium text-ink-1">{m.name}</span>
												<span className="text-ink-3 text-xs">
													{KIND_LABEL[m.kind]}
												</span>
											</span>
										</TableCell>
										<TableCell
											label="Where"
											className="font-mono text-ink-3 text-xs"
										>
											{whereOf(m)}
										</TableCell>
										<TableCell label="Reference">
											<span className="flex items-center gap-1">
												<code className="type-mono max-w-[260px] truncate text-ink-3 text-xs">
													{m.example}
												</code>
												<CopyButton value={m.example} label="Copy example" />
											</span>
										</TableCell>
										<TableCell label="Available to">
											<span className="text-ink-2 text-xs">
												{scopeSummary(m)}
											</span>
										</TableCell>
										<TableCell label="Test">
											<TestState manager={m} />
										</TableCell>
										<TableCell pin="end">
											<span className="flex justify-end gap-1">
												<Button
													variant="ghost"
													size="sm"
													onClick={() => setTesting(m)}
												>
													Test
												</Button>
												<Button
													variant="ghost"
													size="sm"
													onClick={() => setEditing(m)}
												>
													Edit
												</Button>
												<Button
													variant="ghost"
													size="sm"
													className="text-destructive-text"
													onClick={() =>
														void confirm({
															title: `Remove ${m.name}?`,
															body: "A secret manager that a variable still references can not be removed. Nothing is deleted in the secret manager itself.",
															cta: "Remove secret manager",
															danger: true,
															hold: false,
															onConfirm: async () => {
																await remove.mutateAsync(m);
															},
														})
													}
												>
													Remove
												</Button>
											</span>
										</TableCell>
									</TableRow>
								))}
							</TableBody>
						</Table>
					</div>
				</>
			)}
			<SecretManagerDialog
				open={creating || editing !== null}
				onOpenChange={(open) => {
					if (!open) {
						setCreating(false);
						setEditing(null);
					}
				}}
				manager={editing}
			/>
			<TestDialog manager={testing} onClose={() => setTesting(null)} />
		</div>
	);
}
