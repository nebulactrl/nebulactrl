import { describe, expect, test } from "bun:test";
import { createElement, Fragment } from "react";
import { faqItems } from "../content/faq.tsx";
import { plain } from "./faq.tsx";

describe("plain", () => {
	test("keeps the author's text, with no entity or tag handling", () => {
		const node = createElement(
			Fragment,
			null,
			"Tom & Jerry's ",
			createElement("a", { href: "/x" }, '"quoted" &amp; <b>'),
			[" and ", 2],
		);
		expect(plain(node)).toBe(`Tom & Jerry's "quoted" &amp; <b> and 2`);
	});

	test("collapses whitespace across elements", () => {
		const node = createElement(
			"p",
			null,
			"a\n  ",
			createElement("code", null, " b "),
			"\tc",
		);
		expect(plain(node)).toBe("a b c");
	});

	test("every FAQ answer yields prose", () => {
		for (const item of faqItems()) {
			expect(plain(item.a).length, item.q).toBeGreaterThan(20);
		}
	});
});
