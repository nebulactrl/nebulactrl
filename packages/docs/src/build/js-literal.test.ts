import { describe, expect, test } from "bun:test";
import { jsLiteral } from "./js-literal.ts";

describe("jsLiteral", () => {
	test("round-trips through the JavaScript parser", () => {
		const value = {
			slug: 'a/"b"\\c',
			html: "</script><!-- <b>",
			sep: "line\u2028para\u2029end",
			list: [1, null, true],
		};
		expect(new Function(`return ${jsLiteral(value)}`)()).toEqual(value);
	});

	test("never emits a markup or line-separator character", () => {
		const text = jsLiteral("</script>\u2028\u2029<!--");
		expect(text).not.toMatch(/[<>\u2028\u2029]/);
	});
});
