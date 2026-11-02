import { describe, expect, test } from "bun:test";
import { useQuery } from "@tanstack/react-query";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { checkBlocker } from "./model";
import { serverUnrestrictedQuery, useStackCheck } from "./queries";

const CHECK_ROUTE = "POST /environments/:environmentId/stacks/check";

const OK = {
	components: [{ name: "web", index: 0, image: "nginx", ports: [80] }],
	violations: [],
	notes: ["note"],
	unrestricted: false,
	composeAvailable: true,
};

type Input = Parameters<typeof useStackCheck>[0];

function Check(input: Input) {
	const state = useStackCheck(input);
	return (
		<output aria-label="state">
			{JSON.stringify({ ...state, blocker: checkBlocker(state) })}
		</output>
	);
}

function Typing() {
	const [compose, setCompose] = useState("first");
	return (
		<>
			<button type="button" onClick={() => setCompose("second")}>
				change
			</button>
			<Check environmentId="env-1" compose={compose} serverId="" />
		</>
	);
}

function Reader({ serverId }: { serverId: string }) {
	const query = useQuery(serverUnrestrictedQuery(serverId));
	return (
		<output aria-label="out">
			{query.data ? JSON.stringify(query.data) : "loading"}
		</output>
	);
}

function read(): {
	empty: boolean;
	pending: boolean;
	result: { components: { name: string }[]; notes: string[] } | null;
	error: string | null;
	blocker: string | null;
} {
	return JSON.parse(screen.getByLabelText("state").textContent ?? "");
}

describe("useStackCheck", () => {
	test("sends nothing while the text is empty and reports it as empty", async () => {
		const api = stubApi({});
		await renderWithProviders(
			<Check environmentId="env-1" compose={"  \n"} serverId="" />,
		);
		expect(read()).toMatchObject({
			empty: true,
			pending: false,
			result: null,
			error: null,
			blocker: "Add a Compose file.",
		});
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});

	test("checks the first text at once and reports the answer", async () => {
		const api = stubApi({ [CHECK_ROUTE]: { body: OK } });
		await renderWithProviders(
			<Check environmentId="env-1" compose="services: {}" serverId="srv-1" />,
		);
		expect(read().pending).toBe(true);
		await waitFor(() => expect(read().result).not.toBeNull());
		expect(read()).toMatchObject({
			pending: false,
			error: null,
			blocker: null,
			result: { components: [{ name: "web" }], notes: ["note"] },
		});
		expect(api.calls).toEqual([
			{
				method: "POST",
				path: "/environments/env-1/stacks/check",
				query: expect.anything(),
				body: { compose: "services: {}", serverId: "srv-1" },
			},
		]);
	});

	test("a changed text waits to rest before it is sent, keeping the last answer meanwhile", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			[CHECK_ROUTE]: (request) =>
				request
					.clone()
					.json()
					.then((body: { compose: string }) =>
						Response.json({
							...OK,
							components: [{ name: body.compose, index: 0 }],
						}),
					),
		});
		await renderWithProviders(<Typing />);
		await waitFor(() =>
			expect(read().result?.components[0]?.name).toBe("first"),
		);
		await user.click(screen.getByRole("button", { name: "change" }));
		// Still the old answer, now marked as pending; the new text is not sent yet.
		expect(read().pending).toBe(true);
		expect(read().result?.components[0]?.name).toBe("first");
		expect(read().blocker).toBe("Checking the Compose file…");
		expect(api.calls).toHaveLength(1);
		await waitFor(
			() => expect(read().result?.components[0]?.name).toBe("second"),
			{ timeout: 3000 },
		);
		expect(read().pending).toBe(false);
		expect(
			api.calls.map((call) => (call.body as { compose: string }).compose),
		).toEqual(["first", "second"]);
	});

	test("leaves serverId out of the request when no server is chosen", async () => {
		const api = stubApi({ [CHECK_ROUTE]: { body: OK } });
		await renderWithProviders(
			<Check environmentId="env-1" compose="a: 1" serverId="" />,
		);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(api.calls[0]?.body).toEqual({ compose: "a: 1" });
	});

	test("fills in empty lists when the server omits them", async () => {
		stubApi({
			[CHECK_ROUTE]: {
				body: {
					components: null,
					violations: null,
					notes: null,
					unrestricted: true,
					composeAvailable: null,
				},
			},
		});
		await renderWithProviders(
			<Check environmentId="env-1" compose="a: 1" serverId="" />,
		);
		await waitFor(() => expect(read().result).not.toBeNull());
		expect(read().result).toMatchObject({
			components: [],
			violations: [],
			notes: [],
			unrestricted: true,
			composeAvailable: null,
		});
		expect(read().blocker).toBe("The Compose file declares no services.");
	});

	test("a failed check surfaces the server's message", async () => {
		stubApi({
			[CHECK_ROUTE]: problem(422, {
				title: "Unprocessable Entity",
				status: 422,
				detail: "yaml: line 3: mapping values are not allowed here",
			}),
		});
		await renderWithProviders(
			<Check environmentId="env-1" compose="a: : 1" serverId="" />,
		);
		await waitFor(() => expect(read().error).not.toBeNull());
		expect(read().error).toContain("mapping values are not allowed");
		expect(read().pending).toBe(false);
		expect(read().blocker).toBe(read().error);
	});

	test("enabled=false sends nothing and is never pending", async () => {
		const api = stubApi({});
		await renderWithProviders(
			<Check
				environmentId="env-1"
				compose="a: 1"
				serverId=""
				enabled={false}
			/>,
		);
		expect(read()).toMatchObject({ pending: false, result: null });
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});
});

describe("serverUnrestrictedQuery", () => {
	test("reads GET /servers/{id}/unrestricted", async () => {
		const body = make("UnrestrictedSettings", {
			unrestricted: true,
			composeVersion: "2.29.1",
			available: true,
			stacks: 0,
			dockerServices: 0,
		});
		const api = stubApi({
			"GET /servers/:serverId/unrestricted": { body },
		});
		await renderWithProviders(<Reader serverId="srv-7" />);
		await waitFor(() =>
			expect(screen.getByLabelText("out")).toHaveTextContent("2.29.1"),
		);
		expect(api.calls.map((call) => `${call.method} ${call.path}`)).toEqual([
			"GET /servers/srv-7/unrestricted",
		]);
	});
});
