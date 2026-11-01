import { describe, expect, test } from "bun:test";
import { decodeEntities, stripTags, visibleText } from "./test-html.ts";

describe("test-html", () => {
	test("decodeEntities decodes once, so an escaped entity stays text", () => {
		expect(decodeEntities("a &amp; b &#x27;c&#x27; &quot;d&quot;")).toBe(
			`a & b 'c' "d"`,
		);
		expect(decodeEntities("&amp;quot; &amp;#x27; &amp;amp;")).toBe(
			"&quot; &#x27; &amp;",
		);
	});

	test("stripTags cannot leave a tag built from the pieces of another", () => {
		expect(stripTags("<scr<b>ipt>x")).toBe("ipt>x");
		expect(stripTags("<<<b>>>x</b>")).not.toContain("<");
		expect(stripTags("<p>a</p><p>b</p>", " ")).toBe(" a  b ");
	});

	test("visibleText drops scripts, code and attributes", () => {
		const page =
			'<p class="x">Tom &amp; Jerry&#x27;s</p><script>var a = 1</script><pre>$ ls</pre><code>x</code> end';
		expect(visibleText(page)).toBe(" Tom & Jerry's end");
	});
});
