// Text a client controls (a request's path, user agent and host, the values of
// facets built from them) must not steer how the page is drawn: a bidi control
// reverses the text around it, a C0 or C1 control hides or rewrites it. They are
// shown as an escape (`\u202E`) in a muted style instead of being interpreted.

/** C0 and C1 controls, the Arabic letter mark, and the bidi marks, embeddings, overrides and isolates. */
const CONTROL_CLASS =
	"\\u0000-\\u001F\\u007F-\\u009F\\u061C\\u200E\\u200F\\u202A-\\u202E\\u2066-\\u2069";
/** The capture makes `split` keep the controls between the pieces. */
const CONTROLS = new RegExp(`([${CONTROL_CLASS}])`);

function hasControls(text: string): boolean {
	return text.split(CONTROLS).length > 1;
}

function escapeOf(char: string): string {
	const code = char.codePointAt(0) ?? 0;
	return `\\u${code.toString(16).toUpperCase().padStart(4, "0")}`;
}

/** `text` with each control or bidi character written as its `\uXXXX` escape. For titles, labels and anything that is not JSX. */
export function escapeControls(text: string): string {
	if (!hasControls(text)) return text;
	return text
		.split(CONTROLS)
		.map((part, index) => (index % 2 === 1 ? escapeOf(part) : part))
		.join("");
}

/** A stretch of a text, `[start, end)`, that is either plain or one control character. */
export interface TextSegment {
	start: number;
	end: number;
	control: boolean;
}

/**
 * `text` cut around its control and bidi characters, offsets kept: the plain
 * stretches are drawn (and marked) as they are, each control one with
 * `ControlMark`. `keepTabs` leaves a tab inside its plain stretch, for lines
 * whose indentation is part of what they say.
 */
export function controlSegments(
	text: string,
	{ keepTabs = false }: { keepTabs?: boolean } = {},
): TextSegment[] {
	const out: TextSegment[] = [];
	let at = 0;
	for (const [index, part] of text.split(CONTROLS).entries()) {
		const end = at + part.length;
		const control = index % 2 === 1 && !(keepTabs && part === "\t");
		const last = out[out.length - 1];
		if (last && !last.control && !control) last.end = end;
		else if (part !== "") out.push({ start: at, end, control });
		at = end;
	}
	return out;
}

/** One control or bidi character shown as its muted `\uXXXX` escape. */
export function ControlMark({ char }: { char: string }) {
	return <span className="text-ink-4">{escapeOf(char)}</span>;
}

/**
 * `text` for display: control and bidi characters appear as muted `\uXXXX`
 * escapes. `layout` keeps tabs and line breaks as they are, for a block of
 * text that is meant to span lines.
 */
export function SafeText({
	text,
	layout = false,
}: {
	text: string;
	layout?: boolean;
}) {
	if (!hasControls(text)) return <>{text}</>;
	return (
		<>
			{text.split(CONTROLS).map((part, index) =>
				index % 2 === 1 && !(layout && /[\t\n\r]/.test(part)) ? (
					// biome-ignore lint/suspicious/noArrayIndexKey: the parts are positional
					<ControlMark key={index} char={part} />
				) : (
					part
				),
			)}
		</>
	);
}
