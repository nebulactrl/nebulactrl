import { useQuery } from "@tanstack/react-query";
import type { Terminal as XTerm } from "@xterm/xterm";
import {
	CopyIcon,
	FolderOpenIcon,
	PowerIcon,
	TerminalIcon,
} from "lucide-react";
import {
	useCallback,
	useEffect,
	useId,
	useReducer,
	useRef,
	useState,
} from "react";
import { errorMessage } from "#/api/client-config";
import { serviceQuery } from "#/api/hooks";
import { useConfirm } from "#/components/common/confirm-dialog";
import { ErrorState } from "#/components/common/error-state";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { Field, FieldDescription, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { StatusPill } from "#/components/ui/lamp";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import {
	Sheet,
	SheetBody,
	SheetContent,
	SheetDescription,
	SheetHeader,
	SheetTitle,
} from "#/components/ui/sheet";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { splitCommand } from "#/features/service-settings/draft";
import { replicaLabel, stackComponents } from "#/features/stacks/model";
import { LiveTerminal } from "./live-terminal";
import {
	buildOpenMessage,
	describeEnd,
	errorTitle,
	IDLE_TIMEOUT_MINUTES,
	idleCountdown,
	terminalSocketUrl,
} from "./protocol";
import { useTerminalInfo } from "./queries";
import { reduceSession, SETUP, type SessionView } from "./session-state";
import {
	closeShell,
	openFiles,
	useShellProcess,
	useShellReplica,
	useShellTarget,
} from "./store";
import {
	replicaProcesses,
	replicasOfProcess,
	startProcess,
	startReplica,
	targetLabel,
} from "./targets";

interface StartOptions {
	/** The process whose replica to open; "" for a service without processes. */
	process: string;
	replica: number;
	command: string[];
	confirmProduction: boolean;
}

/** The status line under the terminal: one sentence per phase. */
function statusText(view: SessionView, process: string): string {
	switch (view.phase) {
		case "setup":
			return "Not connected.";
		case "connecting":
			return "Connecting…";
		case "live":
			return `Connected to ${process === "" ? `replica ${view.replica}` : targetLabel(process, view.replica)} · ${view.container}`;
		case "ended":
			return describeEnd(view.reason, view.exitCode);
		case "failed":
			return `${errorTitle(view.code)}: ${view.message}`;
	}
}

function pillFor(view: SessionView) {
	switch (view.phase) {
		case "live":
			return { kind: "live", label: "Connected" } as const;
		case "connecting":
			return { kind: "building", label: "Connecting" } as const;
		case "failed":
			return { kind: "failed", label: "Failed" } as const;
		case "ended":
			return { kind: "stopped", label: "Ended" } as const;
		default:
			return { kind: "offline", label: "Not connected" } as const;
	}
}

function SetupForm({
	serviceId,
	onStart,
}: {
	serviceId: string;
	onStart: (o: StartOptions) => void;
}) {
	const info = useTerminalInfo(serviceId);
	const confirm = useConfirm();
	const service = useQuery(serviceQuery(serviceId));
	const processId = useId();
	const replicaId = useId();
	const commandId = useId();
	const preselected = useShellReplica();
	const preselectedProcess = useShellProcess();
	const [processPick, setProcessPick] = useState<string | null>(null);
	const [replica, setReplica] = useState<number | null>(preselected);
	const components = service.data ? stackComponents(service.data) : [];
	const [command, setCommand] = useState("");

	const everyReplica = info.data?.replicas ?? [];
	const processes = replicaProcesses(everyReplica);
	// Replicas are numbered inside their process: pick the process first.
	const process =
		processes.length === 0
			? ""
			: (processPick ?? startProcess(everyReplica, preselectedProcess));
	const ofProcess = replicasOfProcess(everyReplica, process);
	const running = ofProcess.filter((r) => r.running);
	const chosen = replica ?? startReplica(everyReplica, process, null);

	if (info.isPending) {
		return (
			<div className="flex flex-col gap-3 p-4" role="status">
				<Skeleton className="h-9 rounded-lg" />
				<Skeleton className="h-9 rounded-lg" />
			</div>
		);
	}
	if (info.isError || !info.data) {
		return (
			<div className="p-4">
				<ErrorState
					title="The terminal is not available"
					body={
						info.error
							? errorMessage(info.error)
							: "The control plane did not answer."
					}
					retry={() => void info.refetch()}
				/>
			</div>
		);
	}
	const data = info.data;
	const tooMany = data.activeSessions >= data.maxSessions;
	const argv = splitCommand(command);

	const open = async () => {
		if (data.production) {
			const ok = await confirm({
				title: `Open a terminal in production${service.data ? `: ${service.data.name}` : ""}?`,
				body: (
					<>
						You are about to get a shell inside a running production container.
						Anything you change there lasts until the next deployment, and it
						can see the service's secret variables. The session is recorded in
						the audit log (who, which replica, how long) but never what you type
						or read.
					</>
				),
				cta: "Open terminal",
				danger: true,
			});
			if (!ok) return;
		}
		onStart({
			process,
			replica: chosen,
			command: argv,
			confirmProduction: data.production,
		});
	};

	return (
		<div className="flex flex-col gap-4 p-4">
			{data.available ? null : (
				<Alert variant="warning" role="status">
					<AlertDescription>
						{data.reason ?? "A terminal cannot be opened right now."}
					</AlertDescription>
				</Alert>
			)}
			{processes.length > 0 ? (
				<Field>
					<FieldLabel htmlFor={processId}>Process</FieldLabel>
					<Select
						value={process}
						items={processes.map((name) => ({ value: name, label: name }))}
						onValueChange={(next) => {
							if (next === null) return;
							setProcessPick(next);
							setReplica(null);
						}}
					>
						<SelectTrigger
							id={processId}
							className="w-full max-w-[360px] font-mono"
						>
							<SelectValue />
						</SelectTrigger>
						<SelectContent>
							{processes.map((name) => (
								<SelectItem key={name} value={name} className="font-mono">
									{name}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
				</Field>
			) : null}
			<Field>
				<FieldLabel htmlFor={replicaId}>
					{components.length > 0 ? "Component" : "Replica"}
				</FieldLabel>
				<Select
					value={String(chosen)}
					items={ofProcess.map((r) => ({
						value: String(r.index),
						label: `${replicaLabel(components, r.index)} · ${r.state}`,
					}))}
					onValueChange={(v) => setReplica(Number(v))}
				>
					<SelectTrigger id={replicaId} className="w-full max-w-[360px]">
						<SelectValue />
					</SelectTrigger>
					<SelectContent>
						{ofProcess.map((r) => (
							<SelectItem
								key={r.index}
								value={String(r.index)}
								disabled={!r.running}
							>
								{replicaLabel(components, r.index)} · {r.state}
							</SelectItem>
						))}
					</SelectContent>
				</Select>
				<FieldDescription>
					{data.serverName ? `On ${data.serverName}. ` : ""}
					Only running replicas can be opened.
				</FieldDescription>
			</Field>
			<Field>
				<FieldLabel htmlFor={commandId}>Command</FieldLabel>
				<Input
					id={commandId}
					className="font-mono"
					autoComplete="off"
					spellCheck={false}
					placeholder="Default: bash, or sh"
					value={command}
					onChange={(e) => setCommand(e.target.value)}
				/>
				<FieldDescription>
					Leave empty for a shell, or run one program (for example{" "}
					<code className="type-mono">psql -U postgres</code>). It runs as the
					container's own user.
				</FieldDescription>
			</Field>
			<p className="text-ink-3 text-xs">
				Sessions are audited; what you type and see is never recorded. A session
				with no input for {IDLE_TIMEOUT_MINUTES} minutes is closed. You can have{" "}
				{data.maxSessions} open at once
				{data.activeSessions > 0 ? ` (${data.activeSessions} open now)` : ""}.
			</p>
			{tooMany ? (
				<Alert variant="warning">
					<AlertDescription>
						You already have {data.activeSessions} terminals open. Close one
						first.
					</AlertDescription>
				</Alert>
			) : null}
			<div>
				<Button
					variant="primary"
					disabled={!data.available || tooMany || running.length === 0}
					onClick={() => void open()}
				>
					<TerminalIcon aria-hidden />
					{data.production ? "Open in production…" : "Open terminal"}
				</Button>
			</div>
		</div>
	);
}

/**
 * The terminal panel of a service. Mounted once by `TerminalHost`; opened by
 * `openShell`. Closing the panel ends the session.
 */
export function TerminalSheet() {
	const serviceId = useShellTarget();
	const service = useQuery({
		...serviceQuery(serviceId ?? ""),
		enabled: serviceId !== null,
	});
	const [view, dispatch] = useReducer(reduceSession, SETUP);
	const [session, setSession] = useState<{
		id: number;
		start: StartOptions;
	} | null>(null);
	const termRef = useRef<XTerm | null>(null);
	const confirm = useConfirm();
	const counter = useRef(0);

	// A different service (or closing the panel) starts from the setup form.
	// biome-ignore lint/correctness/useExhaustiveDependencies: reset on target change only
	useEffect(() => {
		setSession(null);
		dispatch({ t: "reset" });
	}, [serviceId]);

	const begin = useCallback((start: StartOptions) => {
		counter.current += 1;
		dispatch({ t: "connect" });
		setSession({ id: counter.current, start });
	}, []);

	const startOver = useCallback(() => {
		setSession(null);
		dispatch({ t: "reset" });
	}, []);

	const copySelection = async () => {
		const text = termRef.current?.getSelection() ?? "";
		if (!text) {
			toast.error("Nothing selected", "Select text in the terminal first.");
			return;
		}
		try {
			await navigator.clipboard.writeText(text);
			toast.success("Copied", `${text.length} characters`);
		} catch {
			toast.error("Could not copy", "The browser blocked clipboard access.");
		}
	};

	const requestClose = async () => {
		if (view.phase === "live" || view.phase === "connecting") {
			const ok = await confirm({
				title: "End this terminal session?",
				body: "Closing the panel ends the session and stops its shell. Anything running in it is stopped.",
				cta: "End session",
				dismiss: "Keep open",
				danger: true,
				hold: false,
			});
			if (!ok) return;
		}
		closeShell();
	};

	const pill = pillFor(view);

	return (
		<Sheet
			open={serviceId !== null}
			onOpenChange={(open, details) => {
				if (open) return;
				// A stray click outside must not kill a session.
				if (details.reason === "outside-press") {
					details.cancel();
					return;
				}
				void requestClose();
			}}
		>
			<SheetContent className="sm:max-w-[900px]">
				<SheetHeader className="pb-3">
					<SheetTitle className="flex items-center gap-2.5 pr-10">
						{service.data ? `Shell · ${service.data.name}` : "Shell"}
						<StatusPill kind={pill.kind}>{pill.label}</StatusPill>
					</SheetTitle>
					<SheetDescription className="font-sans">
						An interactive shell inside a running replica container.
					</SheetDescription>
				</SheetHeader>
				<SheetBody className="flex min-h-0 flex-col overscroll-contain p-0">
					{serviceId === null ? null : session === null ? (
						<SetupForm serviceId={serviceId} onStart={begin} />
					) : (
						<div className="flex min-h-0 flex-1 flex-col gap-2 p-3">
							{view.phase === "live" && view.idleSecondsLeft !== null ? (
								<p
									role="status"
									className="rounded-md border border-line bg-white/5 px-3 py-1.5 text-ink-2 text-xs"
								>
									No input for a while: this session closes in{" "}
									{idleCountdown(view.idleSecondsLeft)}. Press any key to keep
									it.
								</p>
							) : null}
							<div className="min-h-0 flex-1">
								<LiveTerminal
									key={session.id}
									url={terminalSocketUrl(
										serviceId,
										window.location,
										session.start.process,
									)}
									open={(size) =>
										buildOpenMessage({ ...session.start, ...size })
									}
									view={view}
									dispatch={dispatch}
									termRef={termRef}
								/>
							</div>
							<div className="flex flex-wrap items-center gap-2 text-xs">
								<span
									className="min-w-0 flex-1 truncate text-ink-2"
									role="status"
									aria-live="polite"
								>
									{view.phase === "connecting" ? (
										<Spinner className="mr-1.5 inline size-3" />
									) : null}
									{statusText(view, session.start.process)}
								</span>
								<Button
									variant="ghost"
									size="sm"
									onClick={() => void copySelection()}
								>
									<CopyIcon aria-hidden />
									Copy selection
								</Button>
								<Button
									variant="ghost"
									size="sm"
									onClick={() =>
										openFiles(serviceId, {
											process: session.start.process,
											replica: session.start.replica,
										})
									}
								>
									<FolderOpenIcon aria-hidden />
									Files
								</Button>
								{view.phase === "live" || view.phase === "connecting" ? (
									<Button variant="secondary" size="sm" onClick={startOver}>
										<PowerIcon aria-hidden />
										End session
									</Button>
								) : (
									<Button variant="secondary" size="sm" onClick={startOver}>
										New session
									</Button>
								)}
							</div>
						</div>
					)}
				</SheetBody>
			</SheetContent>
		</Sheet>
	);
}
