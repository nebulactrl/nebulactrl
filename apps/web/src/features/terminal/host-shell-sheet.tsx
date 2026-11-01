import type { Terminal as XTerm } from "@xterm/xterm";
import { CopyIcon, PowerIcon, TerminalIcon } from "lucide-react";
import { useCallback, useEffect, useReducer, useRef, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { useConfirm } from "#/components/common/confirm-dialog";
import { ErrorState } from "#/components/common/error-state";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { StatusPill } from "#/components/ui/lamp";
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
import { LiveTerminal } from "./live-terminal";
import {
	buildHostOpenMessage,
	describeEnd,
	errorTitle,
	hostShellSocketUrl,
	IDLE_TIMEOUT_MINUTES,
	idleCountdown,
} from "./protocol";
import { useHostShellInfo } from "./queries";
import { reduceSession, SETUP, type SessionView } from "./session-state";
import { closeHostShell, useHostShellTarget } from "./store";

/** The status line under the terminal: one sentence per phase. */
function statusText(view: SessionView, serverName: string): string {
	switch (view.phase) {
		case "setup":
			return "Not connected.";
		case "connecting":
			return "Connecting…";
		case "live":
			return `Connected to ${serverName || "the server"}`;
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

/** Before a session: what a host shell is, why it may be unavailable, and the open button. */
function SetupPanel({
	serverId,
	onStart,
}: {
	serverId: string;
	onStart: (confirmProduction: boolean) => void;
}) {
	const info = useHostShellInfo(serverId);
	const confirm = useConfirm();

	if (info.isPending) {
		return (
			<div className="flex flex-col gap-3 p-4" role="status">
				<Skeleton className="h-16 rounded-lg" />
				<Skeleton className="h-9 w-48 rounded-lg" />
			</div>
		);
	}
	if (info.isError || !info.data) {
		return (
			<div className="p-4">
				<ErrorState
					title="The host shell is not available"
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

	const open = async () => {
		if (data.production) {
			const ok = await confirm({
				title: `Open a shell on ${data.serverName} (production)?`,
				body: (
					<>
						Services of a production environment run on this server. You are
						about to get a root shell on the machine itself: anything you change
						there affects every service on it, the Docker daemon and the agent
						included. The session is recorded in the audit log (who, which
						server, how long) but never what you type or read.
					</>
				),
				cta: "Open host shell",
				danger: true,
			});
			if (!ok) return;
		}
		onStart(data.production);
	};

	return (
		<div className="flex flex-col gap-4 p-4">
			<Alert variant="warning">
				<AlertTitle>This is a shell on the server itself</AlertTitle>
				<AlertDescription>
					It runs as the agent's user (root on most installs), outside any
					container. Sessions are audited with who, which server and how long;
					what you type and see is never recorded. A session with no input for{" "}
					{IDLE_TIMEOUT_MINUTES} minutes is closed. You can have{" "}
					{data.maxSessions} open at once
					{data.activeSessions > 0 ? ` (${data.activeSessions} open now)` : ""}.
				</AlertDescription>
			</Alert>
			{data.available ? null : (
				<Alert variant="info" role="status">
					<AlertDescription>
						{data.reason ?? "A host shell cannot be opened right now."}
					</AlertDescription>
				</Alert>
			)}
			{tooMany ? (
				<Alert variant="warning">
					<AlertDescription>
						You already have {data.activeSessions} host shells open. Close one
						first.
					</AlertDescription>
				</Alert>
			) : null}
			<div>
				<Button
					variant="primary"
					disabled={!data.available || tooMany}
					onClick={() => void open()}
				>
					<TerminalIcon aria-hidden />
					{data.production ? "Open in production…" : "Open host shell"}
				</Button>
			</div>
		</div>
	);
}

/**
 * The host shell panel of a server (opt-in per server; Owners and Admins with
 * two-factor authentication). Mounted by the server page; opened by
 * `openHostShell`. Closing the panel ends the session.
 */
export function HostShellSheet() {
	const serverId = useHostShellTarget();
	const info = useHostShellInfo(serverId);
	const serverName = info.data?.serverName ?? "";
	const [view, dispatch] = useReducer(reduceSession, SETUP);
	const [session, setSession] = useState<{
		id: number;
		confirmProduction: boolean;
	} | null>(null);
	const termRef = useRef<XTerm | null>(null);
	const confirm = useConfirm();
	const counter = useRef(0);

	// A different server (or closing the panel) starts from the setup panel.
	// biome-ignore lint/correctness/useExhaustiveDependencies: reset on target change only
	useEffect(() => {
		setSession(null);
		dispatch({ t: "reset" });
	}, [serverId]);

	const begin = useCallback((confirmProduction: boolean) => {
		counter.current += 1;
		dispatch({ t: "connect" });
		setSession({ id: counter.current, confirmProduction });
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
				title: "End this host shell session?",
				body: "Closing the panel ends the session and stops its shell. Anything running in it is stopped.",
				cta: "End session",
				dismiss: "Keep open",
				danger: true,
				hold: false,
			});
			if (!ok) return;
		}
		closeHostShell();
	};

	const pill = pillFor(view);

	return (
		<Sheet
			open={serverId !== null}
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
						{serverName ? `Host shell · ${serverName}` : "Host shell"}
						<StatusPill kind={pill.kind}>{pill.label}</StatusPill>
					</SheetTitle>
					<SheetDescription className="font-sans">
						An interactive shell on the server itself.
					</SheetDescription>
				</SheetHeader>
				<SheetBody className="flex min-h-0 flex-col overscroll-contain p-0">
					{serverId === null ? null : session === null ? (
						<SetupPanel serverId={serverId} onStart={begin} />
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
									url={hostShellSocketUrl(serverId, window.location)}
									open={(size) =>
										buildHostOpenMessage({
											...size,
											confirmProduction: session.confirmProduction,
										})
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
									{statusText(view, serverName)}
								</span>
								<Button
									variant="ghost"
									size="sm"
									onClick={() => void copySelection()}
								>
									<CopyIcon aria-hidden />
									Copy selection
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
