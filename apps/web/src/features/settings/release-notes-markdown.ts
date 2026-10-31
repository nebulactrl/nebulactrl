// A small markdown reader for release notes: paragraphs, headings, bullet and
// numbered lists, quotes, fenced code, and inline code, bold, italic and links.
// It returns plain data, never HTML, so the renderer builds React elements and
// nothing in the notes can inject markup. A link survives only as http(s).

export type Inline =
	| { type: "text"; value: string }
	| { type: "code"; value: string }
	| { type: "strong"; children: Inline[] }
	| { type: "em"; children: Inline[] }
	| { type: "link"; href: string; children: Inline[] };

export type Block =
	| { type: "paragraph"; children: Inline[] }
	| { type: "heading"; children: Inline[] }
	| { type: "quote"; children: Inline[] }
	| { type: "list"; ordered: boolean; items: Inline[][] }
	| { type: "code"; value: string };

const WORD = /[\p{L}\p{N}]/u;
const ESCAPABLE = "\\`*_[]()#>-";
const LINK = /^\[([^\]]+)\]\(([^)\s]+)\)/;

/** Only absolute web links are kept; anything else (javascript:, data:, relative) reads as plain text. */
export function isSafeHref(href: string): boolean {
	return /^https?:\/\/[^\s]+$/i.test(href);
}

/** Inline markdown to nodes: `code`, **bold**, *italic* or _italic_, [text](https://…). */
export function parseInline(source: string): Inline[] {
	const out: Inline[] = [];
	let text = "";
	const flush = () => {
		if (text !== "") out.push({ type: "text", value: text });
		text = "";
	};

	let i = 0;
	while (i < source.length) {
		const ch = source.charAt(i);

		if (ch === "\\" && i + 1 < source.length) {
			const next = source.charAt(i + 1);
			if (ESCAPABLE.includes(next)) {
				text += next;
				i += 2;
				continue;
			}
		}

		if (ch === "`") {
			const end = source.indexOf("`", i + 1);
			if (end > i + 1) {
				flush();
				out.push({ type: "code", value: source.slice(i + 1, end) });
				i = end + 1;
				continue;
			}
		}

		if (ch === "[") {
			const match = LINK.exec(source.slice(i));
			const label = match?.[1];
			const href = match?.[2];
			if (match && label !== undefined && href !== undefined) {
				if (isSafeHref(href)) {
					flush();
					out.push({ type: "link", href, children: parseInline(label) });
				} else {
					text += label;
				}
				i += match[0].length;
				continue;
			}
		}

		if (source.startsWith("**", i) || source.startsWith("__", i)) {
			const mark = source.slice(i, i + 2);
			const end = source.indexOf(mark, i + 2);
			if (end > i + 2) {
				flush();
				out.push({
					type: "strong",
					children: parseInline(source.slice(i + 2, end)),
				});
				i = end + 2;
				continue;
			}
		}

		if (ch === "*" || ch === "_") {
			const end = source.indexOf(ch, i + 1);
			const opensWord = i === 0 || !WORD.test(source.charAt(i - 1));
			const closesWord = !WORD.test(source.charAt(end + 1));
			const intraWordOk = ch === "*" || (opensWord && closesWord);
			if (end > i + 1 && !/\s/.test(source.charAt(i + 1)) && intraWordOk) {
				flush();
				out.push({
					type: "em",
					children: parseInline(source.slice(i + 1, end)),
				});
				i = end + 1;
				continue;
			}
		}

		text += ch;
		i += 1;
	}
	flush();
	return out;
}

const FENCE = /^\s*```/;
const HEADING = /^\s{0,3}#{1,6}\s+(.*?)\s*#*\s*$/;
const RULE = /^\s{0,3}([-*_])(\s*\1){2,}\s*$/;
const BULLET = /^\s*[-*+]\s+(.*)$/;
const NUMBERED = /^\s*\d+[.)]\s+(.*)$/;
const QUOTE = /^\s{0,3}>\s?(.*)$/;
const INDENTED = /^\s{2,}\S/;

/** Block markdown to nodes. Nested lists flatten to one level; tables and raw HTML read as paragraph text. */
export function parseMarkdown(source: string): Block[] {
	const lines = source.replace(/\r\n?/g, "\n").split("\n");
	const blocks: Block[] = [];
	let paragraph: string[] = [];

	const flushParagraph = () => {
		if (paragraph.length > 0) {
			blocks.push({
				type: "paragraph",
				children: parseInline(paragraph.join(" ")),
			});
		}
		paragraph = [];
	};

	let i = 0;
	while (i < lines.length) {
		const line = lines[i] ?? "";

		if (FENCE.test(line)) {
			flushParagraph();
			const code: string[] = [];
			i += 1;
			while (i < lines.length && !FENCE.test(lines[i] ?? "")) {
				code.push(lines[i] ?? "");
				i += 1;
			}
			i += 1; // the closing fence, if there was one
			blocks.push({ type: "code", value: code.join("\n") });
			continue;
		}

		if (line.trim() === "") {
			flushParagraph();
			i += 1;
			continue;
		}

		if (RULE.test(line)) {
			flushParagraph();
			i += 1;
			continue;
		}

		const heading = HEADING.exec(line);
		if (heading) {
			flushParagraph();
			blocks.push({
				type: "heading",
				children: parseInline(heading[1] ?? ""),
			});
			i += 1;
			continue;
		}

		const marker = BULLET.test(line)
			? BULLET
			: NUMBERED.test(line)
				? NUMBERED
				: null;
		if (marker) {
			flushParagraph();
			const items: string[] = [];
			while (i < lines.length) {
				const current = lines[i] ?? "";
				const item = marker.exec(current);
				if (item) {
					items.push((item[1] ?? "").trim());
				} else if (items.length > 0 && INDENTED.test(current)) {
					const last = items.length - 1;
					items[last] = `${items[last] ?? ""} ${current.trim()}`;
				} else {
					break;
				}
				i += 1;
			}
			blocks.push({
				type: "list",
				ordered: marker === NUMBERED,
				items: items.map(parseInline),
			});
			continue;
		}

		if (QUOTE.test(line)) {
			flushParagraph();
			const quoted: string[] = [];
			while (i < lines.length) {
				const match = QUOTE.exec(lines[i] ?? "");
				if (!match) break;
				quoted.push((match[1] ?? "").trim());
				i += 1;
			}
			blocks.push({
				type: "quote",
				children: parseInline(quoted.join(" ")),
			});
			continue;
		}

		paragraph.push(line.trim());
		i += 1;
	}
	flushParagraph();
	return blocks;
}
