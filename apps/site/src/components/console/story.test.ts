import { describe, expect, test } from "bun:test";
import type { PlayEnv } from "../../islands/motion/play-once.ts";
import { playOnce } from "../../islands/motion/play-once.ts";
import {
	createStory,
	PHASE_MS,
	PRESS_MS,
	type Stage,
	TOML_LINES,
	TYPE_MS,
} from "./story.ts";

/** The story's operations, logged in order. */
function logStage() {
	const log: string[] = [];
	const stage: Stage = {
		snap: (on) => void log.push(`snap:${on}`),
		progress: (n) => void log.push(`progress:${n}`),
		write: (line, typed) => void log.push(`write:${line}:${typed}`),
		caret: (line) => void log.push(`caret:${line}`),
		cursor: (mode) => void log.push(`cursor:${mode}`),
		press: (on) => void log.push(`press:${on}`),
		phase: (phase, state) => void log.push(`phase:${phase}:${state}`),
	};
	return { log, stage };
}

/** Runs every step of a list in time order, as a clock would. */
function runAll(steps: { at: number; run: () => void }[]) {
	for (const step of [...steps].sort((a, b) => a.at - b.at)) step.run();
}

describe("the nebula.toml the agent writes", () => {
	test("is seven lines that follow the file's rules", () => {
		expect(TOML_LINES).toHaveLength(7);
		// `schema` is required and comes first.
		expect(TOML_LINES[0]).toBe("schema = 1");
		const text = TOML_LINES.join("\n");
		// One source per service, and a table for domains and for variables.
		expect(text).toContain('image = "');
		expect(text).not.toMatch(/^git = /m);
		expect(text).not.toMatch(/^domains = /m);
		expect(text).not.toContain("[services.web.env]");
		expect(text).toContain('[services.web.domains."shop.example.com"]');
		expect(text).toContain("[services.web.variables]");
		// Scalars of a service come before its sub tables.
		const firstTable = TOML_LINES.findIndex(
			(l, i) => i > 1 && l.startsWith("["),
		);
		const lastScalar = TOML_LINES.indexOf("port = 8080");
		expect(lastScalar).toBeLessThan(firstTable);
	});
});

describe("the product story", () => {
	test("it is about twelve seconds: the agent types, plans, stages, asks, waits, the cursor glides, presses, and web goes live, in that order", () => {
		const { log, stage } = logStage();
		const story = createStory(stage);
		expect(story.length).toBeGreaterThan(11000);
		expect(story.length).toBeLessThan(14000);
		runAll(story.steps);

		// The last entry: the reset frame logs `press:false` too, before the story starts.
		const at = (entry: string) => log.lastIndexOf(entry);
		const last = TOML_LINES.length - 1;
		const order = [
			"caret:0",
			`write:0:${TOML_LINES[0]}`,
			`caret:${last}`,
			`write:${last}:${TOML_LINES[last]}`,
			"progress:1",
			"progress:2",
			"progress:3",
			"cursor:glide",
			"press:true",
			"press:false",
			"progress:4",
			"phase:0:live",
			"phase:0:done",
			"phase:1:live",
			"phase:1:done",
			"phase:2:live",
			"phase:2:done",
			"progress:5",
		].map(at);
		for (const i of order) expect(i).toBeGreaterThan(-1);
		expect(order).toEqual([...order].sort((a, b) => a - b));
		expect(log.filter((l) => l.startsWith("progress:"))).toEqual([
			"progress:1",
			"progress:2",
			"progress:3",
			"progress:4",
			"progress:5",
		]);
	});

	test("the first frame is drawn with transitions off, and they come back as the story starts", () => {
		const { log, stage } = logStage();
		const story = createStory(stage);
		story.reset();
		expect(log.slice(0, 2)).toEqual(["snap:true", "progress:0"]);
		expect(log.filter((l) => l.startsWith("write:"))).toHaveLength(
			TOML_LINES.length,
		);
		expect(log).toContain("cursor:rest");
		expect(log.filter((l) => l.startsWith("phase:"))).toEqual([
			"phase:0:off",
			"phase:1:off",
			"phase:2:off",
		]);
		const first = [...story.steps].sort((a, b) => a.at - b.at)[0];
		first?.run();
		expect(log.at(-1)).toBe("snap:false");
	});

	test("the agent types at 20ms a character, each line in order", () => {
		const { stage } = logStage();
		const writes: { at: number; line: number; typed: string }[] = [];
		let now = 0;
		const story = createStory({
			...stage,
			write: (line, typed) => void writes.push({ at: now, line, typed }),
		});
		for (const step of [...story.steps].sort((a, b) => a.at - b.at)) {
			now = step.at;
			step.run();
		}
		expect(TYPE_MS).toBe(20);
		expect(new Set(writes.map((w) => w.line))).toEqual(
			new Set(TOML_LINES.map((_, i) => i)),
		);
		for (const [row, line] of TOML_LINES.entries()) {
			const typed = writes.filter((w) => w.line === row);
			expect(typed).toHaveLength(line.length);
			expect(typed.at(-1)?.typed).toBe(line);
			for (const [n, write] of typed.entries()) {
				const before = typed[n - 1];
				if (before) expect(write.at - before.at).toBe(TYPE_MS);
			}
		}
	});

	test("a click on Approve plays the approval at once: the press, three 600ms phases, live", () => {
		const { log, stage } = logStage();
		const story = createStory(stage);
		runAll(story.approval);
		expect(log).toEqual([
			"cursor:hide",
			"press:true",
			"press:false",
			"progress:4",
			"phase:0:live",
			"phase:0:done",
			"phase:1:live",
			"phase:1:done",
			"phase:2:live",
			"phase:2:done",
			"progress:5",
		]);
		const ats = story.approval.map((s) => s.at);
		expect(ats.slice(2)).toEqual([
			PRESS_MS + PHASE_MS,
			PRESS_MS + PHASE_MS * 2,
			PRESS_MS + PHASE_MS * 3,
		]);
		expect(story.approvalLength).toBe(PRESS_MS + PHASE_MS * 3);
	});

	test("the end state is the finished story, drawn without playing, and it matches the server markup's end", () => {
		const { log, stage } = logStage();
		createStory(stage).end();
		expect(log).not.toContain("snap:true");
		expect(log).not.toContain("progress:0");
		expect(log).toContain(
			`write:${TOML_LINES.length - 1}:${TOML_LINES.at(-1)}`,
		);
		expect(log.filter((l) => l.startsWith("phase:"))).toEqual([
			"phase:0:done",
			"phase:1:done",
			"phase:2:done",
		]);
		expect(log.at(-1)).toBe("progress:5");
	});

	test("it plays once, in view; under reduced motion it draws the end state and nothing else", () => {
		let inView: ((value: boolean) => void) | undefined;
		const timers: { at: number; run: () => void }[] = [];
		const env = (reduced: boolean): PlayEnv => ({
			setTimeout: (run, ms) => {
				timers.push({ at: ms, run });
				return timers.length;
			},
			clearTimeout: () => {},
			observe: (_el, _threshold, onChange) => {
				inView = onChange;
				return () => {};
			},
			reduced: () => reduced,
		});

		const played = logStage();
		const story = createStory(played.stage);
		playOnce(
			{} as Element,
			story.steps,
			{ length: story.length, end: story.end },
			env(false),
		);
		expect(played.log).toEqual([]);
		inView?.(true);
		expect(timers.length).toBeGreaterThan(story.steps.length);
		runAll(timers);
		expect(played.log.at(-1)).toBe("progress:5");

		const reduced = logStage();
		const still = createStory(reduced.stage);
		const reference = logStage();
		createStory(reference.stage).end();
		timers.length = 0;
		playOnce({} as Element, still.steps, { end: still.end }, env(true));
		expect(reduced.log).toEqual(reference.log);
		expect(timers).toHaveLength(0);
	});
});
