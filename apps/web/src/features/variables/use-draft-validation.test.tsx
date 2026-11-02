import { describe, expect, it } from "bun:test";
import { act, render, screen, waitFor } from "@testing-library/react";
import type { ValidateVariableResult } from "#/api/gen/types.gen";
import { type ApiCall, stubApi } from "#/test/api";
import { make, type Overrides, problem } from "#/test/factories";
import { useDraftValidation } from "./use-draft-validation";

// Literal `${NAME}` placeholder: the escaped `$` keeps it from being a template placeholder.
const ref = (name: string) => `\${${name}}`;

const SECRET_8 = ref("{secret(8)}");
const NOPE = ref("{nope(1)}");
const ABC = ref("{abc}");
const ONE = ref("{one}");
const TWO = ref("{two}");
const X = ref("{x}");

function Probe({
	serviceId = "svc-1",
	name,
	value,
	isSecret = false,
}: {
	serviceId?: string;
	name: string;
	value: string;
	isSecret?: boolean;
}) {
	const result = useDraftValidation(serviceId, name, value, isSecret);
	return (
		<output aria-label={`result for ${value}`}>
			{result === null
				? "none"
				: `${result.ok ? "ok" : "bad"}:${result.message ?? ""}`}
		</output>
	);
}

const answer = (overrides: Overrides<ValidateVariableResult> = {}) =>
	make("ValidateVariableResult", {
		ok: true,
		containsSecret: false,
		generators: [],
		references: [],
		warnings: [],
		...overrides,
	});

function bodyOf(call: ApiCall | undefined): Record<string, unknown> {
	const body = call?.body;
	if (typeof body !== "object" || body === null) {
		throw new Error("the call has no JSON object body");
	}
	return Object.fromEntries(Object.entries(body));
}

/** Lets pending fetch promises and state updates settle. */
async function flush() {
	await act(async () => {
		for (let i = 0; i < 5; i += 1) await new Promise((r) => setTimeout(r, 0));
	});
}

describe("useDraftValidation", () => {
	it("makes no request for a value without an expression", async () => {
		const api = stubApi({
			"POST /services/:id/variables/validate": { body: answer() },
		});
		// Both drafts start together: the plain one would fire first, so one call
		// proves it stayed quiet.
		render(
			<>
				<Probe name="PLAIN" value="plain text" />
				<Probe name="EXPR" value={SECRET_8} />
			</>,
		);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		await flush();
		expect(api.calls.map((call) => bodyOf(call).key)).toEqual(["EXPR"]);
		expect(screen.getByLabelText("result for plain text")).toHaveTextContent(
			"none",
		);
	});

	it("dry-runs an expression after typing stops and returns the answer", async () => {
		const api = stubApi({
			"POST /services/:id/variables/validate": {
				body: answer({ ok: false, message: "unknown function" }),
			},
		});
		render(<Probe name="TOKEN" value={NOPE} isSecret />);
		expect(screen.getByLabelText(`result for ${NOPE}`)).toHaveTextContent(
			"none",
		);
		await waitFor(() =>
			expect(screen.getByLabelText(`result for ${NOPE}`)).toHaveTextContent(
				"bad:unknown function",
			),
		);
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]?.path).toBe("/services/svc-1/variables/validate");
		expect(api.calls[0]?.body).toEqual({
			key: "TOKEN",
			value: NOPE,
			isSecret: true,
		});
	});

	it("leaves the key out of the request while no name is typed yet", async () => {
		const api = stubApi({
			"POST /services/:id/variables/validate": { body: answer() },
		});
		render(<Probe name="" value={SECRET_8} />);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(api.calls[0]?.body).toEqual({
			value: SECRET_8,
			isSecret: false,
		});
	});

	it("targets the service it was given", async () => {
		const api = stubApi({
			"POST /services/:id/variables/validate": { body: answer() },
		});
		render(<Probe serviceId="svc-9" name="K" value={X} />);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(api.calls[0]?.path).toBe("/services/svc-9/variables/validate");
	});

	it("debounces: only the last value of a burst of edits is checked", async () => {
		const api = stubApi({
			"POST /services/:id/variables/validate": { body: answer() },
		});
		const { rerender } = render(<Probe name="K" value={`\${{a`} />);
		rerender(<Probe name="K" value={`\${{ab`} />);
		rerender(<Probe name="K" value={ABC} />);
		await waitFor(() => expect(api.calls.length).toBeGreaterThan(0));
		await flush();
		expect(api.calls.map((call) => bodyOf(call).value)).toEqual([ABC]);
	});

	it("drops a stale answer as soon as the draft changes", async () => {
		stubApi({
			"POST /services/:id/variables/validate": async (request) => {
				const body = await request.clone().json();
				return Response.json(answer({ message: `for ${body.value}` }));
			},
		});
		const { rerender } = render(<Probe name="K" value={ONE} />);
		await waitFor(() =>
			expect(screen.getByLabelText(`result for ${ONE}`)).toHaveTextContent(
				`for ${ONE}`,
			),
		);
		rerender(<Probe name="K" value={TWO} />);
		// The answer for the previous value must not be shown for the new one.
		expect(screen.getByLabelText(`result for ${TWO}`)).toHaveTextContent(
			"none",
		);
		await waitFor(() =>
			expect(screen.getByLabelText(`result for ${TWO}`)).toHaveTextContent(
				`for ${TWO}`,
			),
		);
	});

	it("shows nothing again once the expression is removed", async () => {
		stubApi({
			"POST /services/:id/variables/validate": {
				body: answer({ message: "m" }),
			},
		});
		const { rerender } = render(<Probe name="K" value={X} />);
		await waitFor(() =>
			expect(screen.getByLabelText(`result for ${X}`)).toHaveTextContent(
				"ok:m",
			),
		);
		rerender(<Probe name="K" value="plain" />);
		expect(screen.getByLabelText("result for plain")).toHaveTextContent("none");
	});

	it("shows nothing when the check itself fails", async () => {
		const api = stubApi({
			"POST /services/:id/variables/validate": problem(500, {
				code: "internal_error",
				title: "boom",
			}),
		});
		render(<Probe name="K" value={X} />);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		await flush();
		expect(screen.getByLabelText(`result for ${X}`)).toHaveTextContent("none");
	});

	it("re-checks when the secret flag changes", async () => {
		const api = stubApi({
			"POST /services/:id/variables/validate": { body: answer() },
		});
		const { rerender } = render(<Probe name="K" value={X} />);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		rerender(<Probe name="K" value={X} isSecret />);
		await waitFor(() => expect(api.calls).toHaveLength(2));
		expect(bodyOf(api.calls[1]).isSecret).toBe(true);
	});
});
