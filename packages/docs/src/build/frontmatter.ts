// A deliberately small frontmatter reader: a `---` fenced block of
// `key: value` lines (strings, quoted strings, integers, booleans). The docs
// only need title, description, section and order, so a YAML dependency would
// buy nothing; anything fancier is an error rather than silently misread.

export type FrontmatterValue = string | number | boolean;

export interface ParsedFrontmatter {
	data: Record<string, FrontmatterValue>;
	/** The markdown after the closing fence. */
	body: string;
	/** Lines the fence block took (so heading line numbers can be reported). */
	lines: number;
}

export function parseFrontmatter(source: string): ParsedFrontmatter {
	const text = source.replace(/^\uFEFF/, "");
	if (!/^---[ \t]*\r?\n/.test(text)) return { data: {}, body: text, lines: 0 };
	const rows = text.split(/\r?\n/);
	let close = -1;
	for (let i = 1; i < rows.length; i++) {
		if (/^---[ \t]*$/.test(rows[i] ?? "")) {
			close = i;
			break;
		}
	}
	if (close < 0) throw new Error("frontmatter is not closed with ---");
	const data: Record<string, FrontmatterValue> = {};
	for (let i = 1; i < close; i++) {
		const row = rows[i] ?? "";
		if (row.trim() === "" || row.trimStart().startsWith("#")) continue;
		const match = /^([A-Za-z][\w-]*):[ \t]*(.*)$/.exec(row);
		if (!match)
			throw new Error(`frontmatter line ${i + 1}: expected "key: value"`);
		const key = match[1] as string;
		if (key in data)
			throw new Error(`frontmatter line ${i + 1}: duplicate key "${key}"`);
		data[key] = parseValue(match[2] as string, i + 1);
	}
	return { data, body: rows.slice(close + 1).join("\n"), lines: close + 1 };
}

function parseValue(raw: string, line: number): FrontmatterValue {
	const value = raw.trim();
	if (value === "") return "";
	const quote = value[0];
	if (quote === '"' || quote === "'") {
		if (value.length < 2 || value[value.length - 1] !== quote) {
			throw new Error(`frontmatter line ${line}: unterminated quoted string`);
		}
		const inner = value.slice(1, -1);
		return quote === '"'
			? inner.replace(/\\(["\\])/g, "$1")
			: inner.replace(/''/g, "'");
	}
	if (value === "true") return true;
	if (value === "false") return false;
	if (/^-?\d+$/.test(value)) return Number(value);
	if (/^[[{|>&*!]/.test(value)) {
		throw new Error(
			`frontmatter line ${line}: lists, maps and block scalars are not supported`,
		);
	}
	return value;
}

/** A page's validated frontmatter. */
export interface DocFrontmatter {
	title: string;
	description: string;
	section: string;
	order: number;
	generated: boolean;
	/** Marks the API reference index: the generated tag list is appended. */
	api: boolean;
}

export function readDocFrontmatter(
	data: Record<string, FrontmatterValue>,
): Partial<DocFrontmatter> {
	const out: Partial<DocFrontmatter> = {};
	const str = (key: string): string | undefined => {
		const value = data[key];
		if (value === undefined) return undefined;
		if (typeof value !== "string" || value.trim() === "") {
			throw new Error(`frontmatter "${key}" must be a non-empty string`);
		}
		return value;
	};
	const title = str("title");
	if (title !== undefined) out.title = title;
	const description = str("description");
	if (description !== undefined) out.description = description;
	const section = str("section");
	if (section !== undefined) out.section = section;
	if (data.order !== undefined) {
		if (typeof data.order !== "number") {
			throw new Error('frontmatter "order" must be an integer');
		}
		out.order = data.order;
	}
	out.generated = data.generated === true;
	out.api = data.api === true;
	return out;
}
