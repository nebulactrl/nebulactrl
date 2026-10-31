import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useState } from "react";
import { getTaskRunOptions } from "#/api/gen/@tanstack/react-query.gen";
import { runServiceCommand } from "#/api/gen/sdk.gen";
import type { RunView } from "#/api/gen/types.gen";
import { serviceQuery } from "#/api/hooks";
import { CodeBlock } from "#/components/common/code-block";
import { Alert, AlertDescription } from "#/components/ui/alert";
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
import {
	Field,
	FieldDescription,
	FieldError,
	FieldLabel,
} from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { StatusPill } from "#/components/ui/lamp";
import { Spinner } from "#/components/ui/spinner";
import {
	commandText,
	DEFAULT_TIMEOUT_SECONDS,
	formatRunDuration,
	isActive,
	parseTimeoutSeconds,
	runPresentation,
	splitCommand,
} from "./model";
import { invalidateTasks, startErrorMessage } from "./queries";
import { RunOutput } from "./run-output";
import { closeRunCommand, useRunCommandTarget } from "./store";

/** The started run, live: status pill, exit code and its output. */
function LiveRun({ started }: { started: RunView }) {
	const run = useQuery({
		...getTaskRunOptions({ path: { runId: started.id } }),
		initialData: started,
		refetchInterval: (q) =>
			isActive(q.state.data?.status ?? started.status) ? 2000 : false,
	});
	const current = run.data;
	const p = runPresentation(current.status);
	const active = isActive(current.status);
	return (
		<div className="flex flex-col gap-3">
			<div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 text-xs">
				<StatusPill tone={p.tone} pulse={p.pulse}>
					{p.label}
				</StatusPill>
				<span className="font-mono text-ink-2" aria-live="polite">
					{active
						? "running…"
						: current.exitCode === undefined
							? "no exit code"
							: `exit ${current.exitCode}`}
				</span>
				<span className="text-ink-3 tabular-nums">
					{formatRunDuration(current)}
				</span>
			</div>
			<CodeBlock copyValue={commandText(current.command)}>
				{commandText(current.command)}
			</CodeBlock>
			{current.error ? (
				<Alert variant="destructive">
					<AlertDescription>{current.error}</AlertDescription>
				</Alert>
			) : null}
			<RunOutput
				key={current.id}
				runId={current.id}
				active={active}
				label="Command output"
				className="h-[320px]"
			/>
		</div>
	);
}

/**
 * Run a one-off command in a fresh container of the service's current image
 * (a migration, a console task). Mounted once by `TasksHost`; opened by
 * `openRunCommand`. After Run it turns into the live output of that run.
 */
export function RunCommandDialog() {
	const serviceId = useRunCommandTarget();
	const queryClient = useQueryClient();
	const service = useQuery({
		...serviceQuery(serviceId ?? ""),
		enabled: serviceId !== null,
	});
	const [command, setCommand] = useState("");
	const [timeout, setTimeoutText] = useState(String(DEFAULT_TIMEOUT_SECONDS));
	const [started, setStarted] = useState<RunView | null>(null);
	const [error, setError] = useState<string | null>(null);

	useEffect(() => {
		if (serviceId === null) return;
		setCommand("");
		setTimeoutText(String(DEFAULT_TIMEOUT_SECONDS));
		setStarted(null);
		setError(null);
	}, [serviceId]);

	const argv = splitCommand(command);
	const timeoutSeconds = parseTimeoutSeconds(timeout);
	const valid = argv.length > 0 && timeoutSeconds !== null;

	const run = useMutation({
		mutationFn: async () => {
			const { data } = await runServiceCommand({
				path: { serviceId: serviceId ?? "" },
				body:
					timeoutSeconds === null
						? { command: argv }
						: { command: argv, timeoutSeconds },
			});
			return data;
		},
		onSuccess: async (data) => {
			setStarted(data);
			await invalidateTasks(queryClient);
		},
		onError: (e) => setError(startErrorMessage(e)),
	});

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (!valid || run.isPending) return;
		setError(null);
		run.mutate();
	};

	const another = () => {
		setStarted(null);
		setError(null);
		run.reset();
	};

	return (
		<Dialog
			open={serviceId !== null}
			onOpenChange={(open) => {
				if (!open) closeRunCommand();
			}}
		>
			<DialogContent size="lg">
				<form onSubmit={submit} className="contents">
					<DialogHeader>
						<DialogTitle>
							{service.data
								? `Run a command in ${service.data.name}`
								: "Run a command"}
						</DialogTitle>
						<DialogDescription>
							Runs once, in a fresh container of the service's current image,
							with its variables and volumes. The running service is not
							touched. The run is recorded in the run history.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						{started ? (
							<LiveRun started={started} />
						) : (
							<>
								<Field>
									<FieldLabel htmlFor="run-command">Command</FieldLabel>
									<Input
										id="run-command"
										className="font-mono"
										autoComplete="off"
										spellCheck={false}
										autoFocus
										placeholder="rake db:migrate"
										value={command}
										onChange={(e) => setCommand(e.target.value)}
									/>
									<FieldDescription>
										Run without a shell, so pipes and && do nothing on their
										own. For shell syntax use{" "}
										<code className="type-mono">sh -c "…"</code>.
									</FieldDescription>
								</Field>
								<Field className="sm:max-w-[200px]">
									<FieldLabel htmlFor="run-timeout">
										Timeout (seconds)
									</FieldLabel>
									<Input
										id="run-timeout"
										inputMode="numeric"
										value={timeout}
										onChange={(e) => setTimeoutText(e.target.value)}
									/>
									<FieldDescription>
										The command is stopped after this long.
									</FieldDescription>
								</Field>
								{error ? <FieldError>{error}</FieldError> : null}
							</>
						)}
					</DialogBody>
					<DialogFooter>
						{started ? (
							<>
								<Button type="button" variant="ghost" onClick={another}>
									Run another
								</Button>
								<Button
									type="button"
									variant="primary"
									onClick={closeRunCommand}
								>
									Close
								</Button>
							</>
						) : (
							<>
								<Button type="button" variant="ghost" onClick={closeRunCommand}>
									Cancel
								</Button>
								<Button
									type="submit"
									variant="primary"
									disabled={!valid || run.isPending}
								>
									{run.isPending ? <Spinner /> : null}
									Run command
								</Button>
							</>
						)}
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
