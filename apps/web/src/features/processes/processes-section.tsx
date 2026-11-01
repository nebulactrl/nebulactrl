import { PlusIcon } from "lucide-react";
import { useMemo } from "react";
import type { Service } from "#/api/models";
import { useConfirm } from "#/components/common/confirm-dialog";
import { Button } from "#/components/ui/button";
import { splitCommand } from "#/features/service-settings/command-line";
import {
	type DraftProps,
	Section,
	SectionBody,
	SettingRow,
	TextRow,
} from "#/features/service-settings/fields";
import {
	MAX_NAMED_PROCESSES,
	newProcessDraft,
	nextProcessName,
	type ProcessDraft,
	processErrors,
} from "./draft";
import {
	commandSummary,
	DEFAULT_PROCESS_NAME,
	defaultProcessName,
	processCountText,
} from "./model";
import { ProcessEditor } from "./process-editor";

const LIMIT_REASON = `A service has at most ${MAX_NAMED_PROCESSES + 1} processes, the default one included. Remove a process to add another.`;

/** Takes the reader to the Start command field, which edits the default process' command. */
function showStartCommand() {
	const field = document.getElementById("set-command");
	if (!field) return;
	field.scrollIntoView({ block: "center" });
	field.focus({ preventScroll: true });
}

/** The domains that serve a saved process: removing it while they exist is refused. */
function domainsServing(service: Service, process: ProcessDraft): string[] {
	if (process.originalName === null) return [];
	return service.domains
		.filter((domain) => domain.process === process.originalName)
		.map((domain) => domain.hostname);
}

/**
 * The processes of one service: the default process (the service's own port
 * and command, named here) and one editable card per additional process. They
 * share one build, image, variables, volumes and release. All of it is part of
 * the settings draft and the save bar, except each process' scale, which
 * applies at once.
 */
export function ProcessesSection({
	service,
	draft,
	errors,
	set,
}: DraftProps & { service: Service }) {
	const confirm = useConfirm();
	const { processes } = draft;
	const defaultName = defaultProcessName({
		processName: draft.processName.trim(),
	});
	const rowErrors = useMemo(
		() =>
			processErrors(processes, {
				defaultName: draft.processName,
				defaultPort: draft.port,
			}),
		[processes, draft.processName, draft.port],
	);
	const atLimit = processes.length >= MAX_NAMED_PROCESSES;
	const command = commandSummary(splitCommand(draft.startCommand));
	const removed = service.processes.filter((process) => process.removed);

	const update = (key: string, patch: Partial<ProcessDraft>) =>
		set(
			"processes",
			processes.map((row) => (row.key === key ? { ...row, ...patch } : row)),
		);

	const add = () => {
		const row = newProcessDraft(
			nextProcessName([defaultName, ...processes.map((p) => p.name)]),
		);
		set("processes", [...processes, row]);
		// The name is prefilled, so the next thing to type is the command.
		requestAnimationFrame(() =>
			document.getElementById(`process-${row.key}-command`)?.focus(),
		);
	};

	const remove = async (row: ProcessDraft) => {
		const serving = domainsServing(service, row);
		const confirmed = await confirm({
			title: `Remove ${row.name || "this process"}?`,
			body:
				serving.length > 0
					? `It stops on the next deploy. ${serving.join(", ")} ${serving.length === 1 ? "serves" : "serve"} this process, and saving is refused until you remove ${serving.length === 1 ? "that domain" : "those domains"} or add them back to another process.`
					: "It stops on the next deploy, and its scale settings are cleared. Nothing changes until you save.",
			cta: "Remove process",
			glyph: "trash",
			danger: true,
			hold: false,
		});
		if (confirmed) {
			set(
				"processes",
				processes.filter((candidate) => candidate.key !== row.key),
			);
		}
	};

	return (
		<Section
			title="Processes"
			description="Run a worker or a scheduler next to the default process, from the same build."
		>
			{processes.length === 0 ? (
				<SectionBody className="flex flex-col items-start gap-3">
					<p className="type-secondary">
						This service runs one process. Add another to run a worker or a
						scheduler from the same build and release, with its own command,
						port and scale.
					</p>
					<Button id="add-process" variant="secondary" size="xs" onClick={add}>
						<PlusIcon aria-hidden />
						Add process
					</Button>
				</SectionBody>
			) : (
				<>
					<SectionBody className="flex flex-col gap-1">
						<p className="type-secondary">
							{processCountText(processes.length + 1)} run from the same build,
							image, variables, volumes and release. Each one has its own
							containers, logs and scale.
						</p>
					</SectionBody>
					<TextRow
						id="set-process-name"
						label="Default process name"
						description="The service's own port and start command run as this process."
						mono
						narrow
						value={draft.processName}
						error={errors.processName}
						placeholder={DEFAULT_PROCESS_NAME}
						onChange={(value) => set("processName", value)}
					/>
					<SettingRow label={defaultName} description="The default process">
						<div className="flex flex-wrap items-center gap-x-3 gap-y-1">
							<span className="font-mono text-[13px] text-ink-2">
								{command}
							</span>
							<span className="text-ink-3 text-xs">
								{draft.port.trim() === ""
									? "no port"
									: `port ${draft.port.trim()}`}
								{draft.singleton ? " · one instance at a time" : ""}
							</span>
							<Button variant="ghost" size="xs" onClick={showStartCommand}>
								Edit in Commands
							</Button>
						</div>
					</SettingRow>
					<SectionBody anchor="process-list">
						<ul
							aria-label="Additional processes"
							className="flex flex-col gap-2"
						>
							{processes.map((row) => (
								<ProcessEditor
									key={row.key}
									service={service}
									process={row}
									errors={rowErrors[row.key] ?? {}}
									inherited={{
										stopGraceSeconds: draft.stopGraceSeconds,
										memoryMb: draft.memoryMb,
										cpus: draft.cpus,
									}}
									pending={
										service.processes.find(
											(candidate) => candidate.name === row.originalName,
										)?.pending ?? false
									}
									onChange={(patch) => update(row.key, patch)}
									onRemove={() => void remove(row)}
								/>
							))}
						</ul>
					</SectionBody>
					<SectionBody className="flex flex-wrap items-center gap-3">
						<Button
							id="add-process"
							variant="secondary"
							size="xs"
							disabled={atLimit}
							title={atLimit ? LIMIT_REASON : undefined}
							onClick={add}
						>
							<PlusIcon aria-hidden />
							Add process
						</Button>
						{atLimit ? (
							<span className="type-secondary">{LIMIT_REASON}</span>
						) : null}
					</SectionBody>
					{removed.length > 0 ? (
						<SectionBody>
							<p className="type-secondary">
								Removed, stops on the next deploy:{" "}
								<span className="font-mono">
									{removed.map((process) => process.name).join(", ")}
								</span>
							</p>
						</SectionBody>
					) : null}
				</>
			)}
		</Section>
	);
}
