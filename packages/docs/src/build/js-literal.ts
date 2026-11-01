const ESCAPES: Record<string, string> = {
	"<": "\\u003C",
	">": "\\u003E",
	"\u2028": "\\u2028",
	"\u2029": "\\u2029",
};

/**
 * `value` as a JavaScript expression that is also safe inside an inline
 * `<script>`. `JSON.stringify` alone leaves `</script>` and the line
 * separators in place; the generated modules are bundled files today, but
 * nothing about the text should depend on that.
 */
export function jsLiteral(value: unknown): string {
	return JSON.stringify(value).replace(
		/[<>\u2028\u2029]/g,
		(char) => ESCAPES[char] ?? char,
	);
}
