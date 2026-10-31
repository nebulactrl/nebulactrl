/** `a "b c" d` → ["a", "b c", "d"]. Quotes group; backslash escapes a quote. */
export function splitCommand(input: string): string[] {
	const args: string[] = [];
	let current = "";
	let quote: '"' | "'" | null = null;
	let started = false;
	for (let index = 0; index < input.length; index++) {
		const char = input[index] as string;
		if (quote) {
			if (char === quote) quote = null;
			else if (
				char === "\\" &&
				quote === '"' &&
				(input[index + 1] === '"' || input[index + 1] === "\\")
			) {
				current += input[index + 1];
				index++;
			} else current += char;
		} else if (char === '"' || char === "'") {
			quote = char;
			started = true;
		} else if (/\s/.test(char)) {
			if (started || current !== "") args.push(current);
			current = "";
			started = false;
		} else {
			current += char;
			started = true;
		}
	}
	if (started || current !== "") args.push(current);
	return args;
}

export function quoteArg(arg: string): string {
	if (arg !== "" && !/[\s"']/.test(arg)) return arg;
	return `"${arg.replaceAll("\\", "\\\\").replaceAll('"', '\\"')}"`;
}
