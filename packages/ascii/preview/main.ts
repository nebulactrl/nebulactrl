/**
 * Dev gallery for the ASCII scenes (`bun run preview`, then http://localhost:4180).
 *
 *   ?scene=eclipse|dial-out|lost-signal|dome|band   (default dome)
 *   &frames=header,mobile        which frames (default both)
 *   &text=0                      hide the sample copy (default shown); text=quiet keeps its
 *                                box (so the quiet region applies) but hides the words
 *   &t=31                        freeze at shader time t seconds (renderStatic)
 *   &rm=1                        force reduced motion: the scene's own still
 *
 * Every frame sits on the site's void colour. The sample copy is real text,
 * marked as the quiet region (`quietRects`) or the panel (`maskRect`), exactly
 * as a page would mark its own. Not part of any product bundle.
 */
import {
	type AsciiEngine,
	createAscii,
	DEFAULT_FONT_FAMILY,
	NEBULA_PALETTE,
	SCENE_NAMES,
	type SceneName,
} from "../src/index";

interface Frame {
	name: "header" | "mobile";
	css: string;
}
const FRAMES: Frame[] = [
	{ name: "header", css: "header" },
	{ name: "mobile", css: "mobile" },
];

const params = new URLSearchParams(location.search);
const scene = (
	SCENE_NAMES.includes(params.get("scene") as SceneName)
		? params.get("scene")
		: "dome"
) as SceneName;
const wanted = (params.get("frames") ?? "header,mobile").split(",");
const textMode = params.get("text") ?? "1";
const withText = textMode !== "0";
const freeze = params.has("t") ? Number(params.get("t")) : null;
const rm = params.get("rm") === "1";

const nav = document.getElementById("nav") as HTMLElement;
for (const name of SCENE_NAMES) {
	const a = document.createElement("a");
	a.href = `?scene=${name}`;
	a.textContent = name;
	if (name === scene) a.setAttribute("aria-current", "page");
	nav.append(a);
}

/** The copy a page would lay over the scene: the quiet region (or, for lost-signal, the panel the gap frames). */
function copyFor(): HTMLElement {
	const el = document.createElement("div");
	if (scene === "lost-signal") {
		el.className = "copy mid";
		el.dataset.asciiPanel = "";
		el.innerHTML = `<p class="code">404</p><h1>This page lost its signal.</h1><p>The link may be old, or the page moved.</p>`;
	} else {
		el.className = "copy left";
		el.dataset.asciiQuiet = "";
		const COPY: Record<string, string> = {
			"dial-out": `<p class="eyebrow">Install</p><h1>Put NebulaCtrl on a host.</h1><p>One binary, one command, a control plane of your own.</p>`,
			band: `<h1>A deploy platform for the servers you already have.</h1><p>Every release proves itself before it takes traffic.</p>`,
		};
		el.innerHTML =
			COPY[scene] ??
			`<p class="eyebrow">Documentation</p><h1>NebulaCtrl documentation</h1><p>Install it, connect servers, deploy, and look up every CLI command and API endpoint.</p>`;
	}
	return el;
}

const engines: AsciiEngine[] = [];
const row = document.getElementById("row") as HTMLElement;
for (const frame of FRAMES) {
	if (!wanted.includes(frame.name)) continue;
	const fig = document.createElement("figure");
	const cap = document.createElement("figcaption");
	const box = document.createElement("div");
	box.className = `frame ${frame.css}`;
	box.dataset.frame = frame.name;
	box.setAttribute("aria-label", `${scene} ${frame.name}`);
	const canvas = document.createElement("canvas");
	canvas.setAttribute("aria-hidden", "true");
	box.append(canvas);
	let copy: HTMLElement | null = null;
	if (withText) {
		copy = copyFor();
		if (textMode === "quiet") copy.style.visibility = "hidden";
		box.append(copy);
	}
	const stat = document.createElement("div");
	stat.className = "stat";
	fig.append(cap, box, stat);
	row.append(fig);
	cap.textContent = `${scene} · ${frame.name} · ${box.clientWidth}×${box.clientHeight}`;

	const engine = createAscii(canvas, {
		scene,
		palette: NEBULA_PALETTE,
		fontFamily: DEFAULT_FONT_FAMILY,
		stats: true,
		...(rm ? { reducedMotion: true } : {}),
		maskRect: () => {
			const panel = copy?.dataset.asciiPanel !== undefined ? copy : null;
			return panel?.getBoundingClientRect() ?? null;
		},
		quietRects: () => {
			const quiet = copy?.dataset.asciiQuiet !== undefined ? copy : null;
			return quiet ? [quiet.getBoundingClientRect()] : [];
		},
	});
	if (!engine) {
		stat.textContent = "WebGL2 unavailable: the halftone floor would show";
		continue;
	}
	engines.push(engine);
	if (freeze !== null) engine.renderStatic(freeze);
	else engine.start();
	box.dataset.ready = "false";
	engine.ready.then(() => {
		box.dataset.ready = "true";
	});
	setInterval(() => {
		const s = engine.stats();
		if (s) {
			stat.textContent = `${s.fps.toFixed(1)} fps · mean ${s.meanMs.toFixed(2)} ms · p95 ${s.p95Ms.toFixed(2)} ms · grid ${s.grid} · cell ${s.cell}`;
		}
	}, 1000);
}

Object.assign(window, { asciiPreview: { engines } });
