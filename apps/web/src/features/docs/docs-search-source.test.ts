import { describe, expect, test } from "bun:test";
import { isInternalSlug } from "./docs-search-source";

describe("isInternalSlug", () => {
	test("keeps the decision log and the architecture notes out of the palette", () => {
		expect(isInternalSlug("decisions/0001-go-modular-monolith")).toBe(true);
		expect(isInternalSlug("architecture/overview")).toBe(true);
	});

	test("keeps the pages that explain the product", () => {
		expect(isInternalSlug("operations/variables")).toBe(false);
		expect(isInternalSlug("security/login-methods")).toBe(false);
		expect(isInternalSlug("")).toBe(false);
	});
});
