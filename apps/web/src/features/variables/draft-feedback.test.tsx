import { describe, expect, it } from "bun:test";
import { render, screen } from "@testing-library/react";
import type { ValidateVariableResult } from "#/api/gen/types.gen";
import { DraftFeedback } from "./draft-feedback";

function result(
	overrides: Partial<ValidateVariableResult> = {},
): ValidateVariableResult {
	return {
		ok: true,
		containsSecret: false,
		generators: [],
		references: [],
		warnings: [],
		...overrides,
	};
}

describe("DraftFeedback", () => {
	it("renders nothing while there is no answer", () => {
		const { container } = render(<DraftFeedback result={null} />);
		expect(container).toBeEmptyDOMElement();
	});

	it("renders nothing for a clean answer", () => {
		const { container } = render(<DraftFeedback result={result()} />);
		expect(container).toBeEmptyDOMElement();
	});

	it("treats null lists like empty ones", () => {
		const { container } = render(
			<DraftFeedback
				result={result({ references: null, warnings: null, generators: null })}
			/>,
		);
		expect(container).toBeEmptyDOMElement();
	});

	it("shows a syntax error with a 1-based character position", () => {
		render(
			<DraftFeedback
				result={result({
					ok: false,
					error: { message: "unexpected }", offset: 6 },
				})}
			/>,
		);
		expect(screen.getByText("unexpected } (character 7)")).toBeInTheDocument();
	});

	it("falls back to the server's message for a failure without a position", () => {
		render(
			<DraftFeedback
				result={result({ ok: false, message: "secret(0) is not allowed" })}
			/>,
		);
		expect(screen.getByText("secret(0) is not allowed")).toBeInTheDocument();
	});

	it("falls back to a generic sentence when a failure says nothing", () => {
		render(<DraftFeedback result={result({ ok: false })} />);
		expect(screen.getByText("This value cannot be used.")).toBeInTheDocument();
	});

	it("does not list warnings or references of a failed draft", () => {
		render(
			<DraftFeedback
				result={result({
					ok: false,
					message: "bad",
					warnings: [{ code: "weak", message: "weak secret" }],
				})}
			/>,
		);
		expect(screen.queryByText("weak secret")).not.toBeInTheDocument();
		expect(screen.queryByRole("list")).not.toBeInTheDocument();
	});

	it("lists warnings", () => {
		render(
			<DraftFeedback
				result={result({
					warnings: [
						{ code: "weak", message: "secret(4) is short" },
						{ code: "invisible", message: "Contains a zero-width space" },
					],
				})}
			/>,
		);
		const items = screen.getAllByRole("listitem");
		expect(items.map((item) => item.textContent)).toEqual([
			"secret(4) is short",
			"Contains a zero-width space",
		]);
	});

	it("says an unresolved reference blocks a deploy, using the server's message when given", () => {
		render(
			<DraftFeedback
				result={result({
					references: [
						{
							ns: "postgres",
							key: "HOST",
							resolved: false,
							secret: false,
							message: "postgres has no HOST",
						},
					],
				})}
			/>,
		);
		expect(
			screen.getByText(
				"postgres has no HOST. A deploy is blocked until it does.",
			),
		).toBeInTheDocument();
	});

	it("names an unresolved reference itself when the server gave no message", () => {
		render(
			<DraftFeedback
				result={result({
					references: [
						{ ns: "redis", key: "URL", resolved: false, secret: false },
						{ key: "OWN", resolved: false, secret: false },
					],
				})}
			/>,
		);
		expect(
			screen.getByText(
				"redis.URL does not resolve yet. A deploy is blocked until it does.",
			),
		).toBeInTheDocument();
		// A reference to the service's own variable has no namespace prefix.
		expect(
			screen.getByText(
				"OWN does not resolve yet. A deploy is blocked until it does.",
			),
		).toBeInTheDocument();
	});

	it("confirms resolved references and flags the ones that carry a secret", () => {
		render(
			<DraftFeedback
				result={result({
					references: [
						{ ns: "postgres", key: "HOST", resolved: true, secret: false },
						{ ns: "postgres", key: "PASSWORD", resolved: true, secret: true },
						{ key: "LOCAL", resolved: true, secret: false },
					],
				})}
			/>,
		);
		const items = screen.getAllByRole("listitem").map((i) => i.textContent);
		expect(items).toEqual([
			"postgres.HOST",
			"postgres.PASSWORD · contains a secret",
			"LOCAL",
		]);
	});

	it("orders warnings, then unresolved, then resolved references", () => {
		render(
			<DraftFeedback
				result={result({
					warnings: [{ code: "w", message: "careful" }],
					references: [
						{ ns: "a", key: "OK", resolved: true, secret: false },
						{ ns: "b", key: "MISSING", resolved: false, secret: false },
					],
				})}
			/>,
		);
		const items = screen.getAllByRole("listitem").map((i) => i.textContent);
		expect(items).toEqual([
			"careful",
			"b.MISSING does not resolve yet. A deploy is blocked until it does.",
			"a.OK",
		]);
	});
});
