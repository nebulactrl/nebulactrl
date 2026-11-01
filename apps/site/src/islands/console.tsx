import { useEffect, useRef } from "react";
import { ConsoleWindow } from "../components/console/console-window.tsx";
import {
	createStory,
	type LineIndex,
	type Phase,
	type Stage,
} from "../components/console/story.ts";
import { playOnce } from "./motion/play-once.ts";

/** The text nodes of one typed line, with the text each ends with. */
interface Written {
	nodes: Text[];
	full: string[];
}

function textOf(line: Element): Written {
	const nodes: Text[] = [];
	const walker = document.createTreeWalker(
		line.firstElementChild as Element,
		NodeFilter.SHOW_TEXT,
	);
	for (let n = walker.nextNode(); n; n = walker.nextNode())
		nodes.push(n as Text);
	return { nodes, full: nodes.map((node) => node.data) };
}

/**
 * The replica's DOM as the story's `Stage`. Everything the story changes is an
 * attribute or the text of a node the server already rendered, so nothing is
 * created, removed or re-laid out: the replica never changes size.
 */
export function bindStage(el: HTMLElement): Stage {
	const lines = [...el.querySelectorAll<HTMLElement>("[data-line]")];
	const written = lines.map(textOf);
	const fills = [...el.querySelectorAll<HTMLElement>(".phase-strip > i")];
	const cursor = el.querySelector<HTMLElement>(".con-cursor");
	const approve = el.querySelector<HTMLElement>(".con-approve");

	/** Where the cursor's tip lands on Approve, in the stage's own coordinates. */
	const target = () => {
		if (!approve) return { x: 0, y: 0 };
		const stage = el.getBoundingClientRect();
		const key = approve.getBoundingClientRect();
		return {
			x: key.left - stage.left + key.width * 0.62,
			y: key.top - stage.top + key.height * 0.58,
		};
	};

	return {
		snap: (on) => el.toggleAttribute("data-snap", on),
		progress: (n) => {
			el.dataset.t = String(n);
			el.style.setProperty("--t", String(n));
		},
		write(line: LineIndex, typed: string) {
			const { nodes, full } = written[line] as Written;
			let left = typed.length;
			nodes.forEach((node, i) => {
				node.data = (full[i] as string).slice(0, Math.max(0, left));
				left -= (full[i] as string).length;
			});
		},
		caret(line) {
			lines.forEach((node, i) => {
				node.toggleAttribute("data-caret", i === line);
			});
		},
		cursor(mode) {
			if (!cursor) return;
			const { x, y } = target();
			if (mode === "rest")
				cursor.style.transform = `translate(${x + 72}px, ${y + 84}px)`;
			if (mode === "glide")
				cursor.style.transform = `translate(${x}px, ${y}px)`;
			cursor.dataset.on = String(mode === "glide");
		},
		press: (on) => el.toggleAttribute("data-press", on),
		phase(phase: Phase, state) {
			(fills[phase] as HTMLElement).dataset.s = state;
			// The card's pill follows the phase, as the product's does: Building, then Deploying, then Live.
			if (state === "live")
				el.dataset.pill = phase === 0 ? "building" : "deploying";
			else if (state === "off" && phase === 0) el.dataset.pill = "building";
			else if (state === "done" && phase === 2) delete el.dataset.pill;
		},
	};
}

/**
 * The product replica. The server renders the end of the story; this plays it
 * once, the first time the replica is in view (the page's one story at a
 * time): the agent types `nebula.toml`, plans, stages and asks to apply, the
 * approval page waits for you and, if you do not click Approve, a ghost cursor
 * presses it; then Build, Release and Verify fill and `web` is live. A click on
 * Approve plays the approval at once. Under reduced motion it plays nothing.
 */
export default function ProductConsole() {
	const stage = useRef<HTMLDivElement>(null);
	useEffect(() => {
		const el = stage.current;
		if (!el) return;
		const story = createStory(bindStage(el));
		const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;
		let timers: number[] = [];

		// The server markup is the live frame. Draw the first frame before the
		// replica shows its content, so the live state never flashes first.
		if (!reduced) story.reset();
		el.closest(".con")?.removeAttribute("data-boot");
		// Stacked on a phone the replica is taller than the screen, so a quarter of it in view starts the story.
		let stop: (() => void) | undefined = playOnce(el, story.steps, {
			length: story.length,
			end: story.end,
			threshold: 0.25,
		});

		const approve = el.querySelector<HTMLElement>(".con-approve");
		const onApprove = () => {
			if (el.dataset.t !== "3" || stop === undefined) return;
			stop();
			stop = undefined;
			timers = story.approval.map((step) =>
				window.setTimeout(step.run, step.at),
			);
		};
		approve?.addEventListener("click", onApprove);

		return () => {
			approve?.removeEventListener("click", onApprove);
			stop?.();
			for (const id of timers) window.clearTimeout(id);
		};
	}, []);
	return <ConsoleWindow stage={stage} />;
}
