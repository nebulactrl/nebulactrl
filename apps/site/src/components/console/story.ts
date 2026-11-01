import type { Step } from "../../islands/motion/play-once.ts";

/**
 * The seven lines the agent writes into `nebula.toml`, in the order it types
 * them. The file is valid: `schema` is required, a service has exactly one
 * source (`image`), domains are a table keyed by hostname and variables are
 * `[services.web.variables]` (docs/reference/nebula-toml.md).
 */
export const TOML_LINES = [
	"schema = 1",
	"[services.web]",
	'image = "ghcr.io/acme/shop:1.4"',
	"port = 8080",
	'[services.web.domains."shop.example.com"]',
	"[services.web.variables]",
	// biome-ignore lint/suspicious/noTemplateCurlyInString: a nebula.toml reference, not a JS template.
	'DATABASE_URL = "${{postgres.DATABASE_URL}}"',
] as const;

export type LineIndex = 0 | 1 | 2 | 3 | 4 | 5 | 6;
export type Phase = 0 | 1 | 2;
export type PhaseState = "off" | "live" | "done";

/**
 * How far the story has got, as the replica's CSS reads it (`data-t` and `--t`
 * on the stage): 0 the agent writes the file, 1 it plans, 2 it stages, 3
 * `apply_changes` asks for approval and the approval page waits for a person,
 * 4 approved and deploying, 5 live. Without the attribute the replica is
 * already at 5.
 */
export type Progress = 0 | 1 | 2 | 3 | 4 | 5;

/**
 * What the story does to the replica, as plain operations. The island binds
 * them to the DOM; a test binds them to a log. Nothing here touches the DOM.
 */
export interface Stage {
	/** True: every transition on the stage is off (the first frame is drawn, not animated). */
	snap(on: boolean): void;
	progress(n: Progress): void;
	/** The text the agent has typed so far on a line. */
	write(line: LineIndex, typed: string): void;
	/** Where the agent's caret is; `null` hides it. */
	caret(line: LineIndex | null): void;
	/** The ghost cursor: parked beside Approve, gliding onto it, or gone. */
	cursor(mode: "rest" | "glide" | "hide"): void;
	/** The press on Approve (scale 0.96). */
	press(on: boolean): void;
	phase(phase: Phase, state: PhaseState): void;
}

export interface Story {
	/** The whole story with the ghost cursor approving: ms from its start. */
	steps: Step[];
	/** When the last motion has settled, in ms from the start. */
	length: number;
	/** What happens from the moment Approve is pressed, in ms from the press. A visitor's click plays this. */
	approval: Step[];
	/** When the approval has settled, in ms from the press. */
	approvalLength: number;
	/** The first frame: the file empty, nothing staged. */
	reset(): void;
	/** The end frame: the file written, approved, `web` live. The server markup is this. */
	end(): void;
}

export const TYPE_MS = 20;
/** The pause where the caret drops to the next line. */
export const LINE_MS = 80;
/** The agent's calls appear this far apart. */
export const CALL_MS = 1000;
/** How long Approve waits for the visitor before the ghost cursor takes it. */
export const WAIT_MS = 3500;
/** The ghost cursor's glide onto Approve. */
export const GLIDE_MS = 600;
/** The press, held. */
export const PRESS_MS = 150;
/** One of Build, Release and Verify filling. */
export const PHASE_MS = 600;

const PHASE_IDS = [0, 1, 2] as const;

/** What the replica does from the press on: Build, Release and Verify fill in turn, then `web` is live. */
function approvalSteps(stage: Stage): { steps: Step[]; length: number } {
	const steps: Step[] = [];
	const at = (ms: number, run: () => void) => steps.push({ at: ms, run });
	at(0, () => {
		stage.cursor("hide");
		stage.press(true);
	});
	let t = PRESS_MS;
	at(t, () => {
		stage.press(false);
		stage.progress(4);
		stage.phase(0, "live");
	});
	for (const p of [1, 2] as const) {
		t += PHASE_MS;
		at(t, () => {
			stage.phase((p - 1) as Phase, "done");
			stage.phase(p, "live");
		});
	}
	t += PHASE_MS;
	at(t, () => {
		stage.phase(2, "done");
		stage.progress(5);
	});
	return { steps, length: t };
}

/**
 * The replica as one story of about twelve seconds:
 *
 * 1. the agent types the seven lines of `nebula.toml` (20 ms a character);
 * 2. it plans the file, stages it and calls `apply_changes`, one call a second;
 * 3. NebulaCtrl's approval page shows the request and Approve waits
 *    for the visitor (3.5 s); then the ghost cursor glides to it (600 ms) and
 *    presses it;
 * 4. Build, Release and Verify fill, 600 ms each, and `web` is live.
 *
 * It plays once, when it is in view. A click on Approve plays step 4 at once.
 */
export function createStory(stage: Stage): Story {
	const steps: Step[] = [];
	const at = (ms: number, run: () => void) => steps.push({ at: ms, run });

	// The first frame is drawn with transitions off; they come back just after.
	at(80, () => stage.snap(false));

	// 1. The agent types, line by line.
	let t = 240;
	TOML_LINES.forEach((line, row) => {
		const index = row as LineIndex;
		at(t, () => stage.caret(index));
		for (let i = 1; i <= line.length; i++) {
			at(t + i * TYPE_MS, () => stage.write(index, line.slice(0, i)));
		}
		t += line.length * TYPE_MS + LINE_MS;
	});

	// 2. Plan, stage, ask.
	at(t, () => {
		stage.caret(null);
		stage.progress(1);
	});
	t += CALL_MS;
	at(t, () => stage.progress(2));
	t += CALL_MS;
	at(t, () => stage.progress(3));

	// 3. Waiting for approval: the visitor may click.
	t += WAIT_MS;
	at(t, () => stage.cursor("glide"));
	t += GLIDE_MS;

	// 4. The press, then the deploy.
	const approval = approvalSteps(stage);
	for (const step of approval.steps) at(t + step.at, step.run);
	t += approval.length;

	const reset = () => {
		stage.snap(true);
		stage.progress(0);
		for (const [row] of TOML_LINES.entries()) stage.write(row as LineIndex, "");
		stage.caret(null);
		stage.cursor("rest");
		stage.press(false);
		for (const p of PHASE_IDS) stage.phase(p, "off");
	};

	const end = () => {
		stage.snap(false);
		stage.caret(null);
		stage.cursor("hide");
		stage.press(false);
		for (const [row, line] of TOML_LINES.entries())
			stage.write(row as LineIndex, line);
		for (const p of PHASE_IDS) stage.phase(p, "done");
		stage.progress(5);
	};

	return {
		steps,
		length: t,
		approval: approval.steps,
		approvalLength: approval.length,
		reset,
		end,
	};
}
