import { describe, expect, test } from "bun:test";
import { useQuery } from "@tanstack/react-query";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { serversQuery } from "#/api/hooks";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { type DomainSave, useDomainSave } from "./use-domain-patch";

const servers = { body: make("PageServer") };

/** A button that saves `save` with the service generation 4, and the outcome. */
function Harness({ save }: { save: DomainSave }) {
	const mutation = useDomainSave({ id: "svc-1", generation: 4 });
	// A query the save has to invalidate, so a refetch is observable.
	useQuery(serversQuery());
	return (
		<div>
			<button type="button" onClick={() => mutation.mutate(save)}>
				Save
			</button>
			{mutation.isError ? <p>failed: {mutation.error.message}</p> : null}
			{mutation.isSuccess ? <p>saved</p> : null}
		</div>
	);
}

const serverCalls = (api: { calls: { method: string; path: string }[] }) =>
	api.calls.filter((c) => c.method === "GET" && c.path === "/servers").length;

describe("useDomainSave", () => {
	test("PATCHes the domain with If-Match set to the service generation and toasts", async () => {
		const ifMatch: (string | null)[] = [];
		const api = stubApi({
			"GET /servers": servers,
			"PATCH /domains/:domainId": (request) => {
				ifMatch.push(request.headers.get("If-Match"));
				return Response.json(make("DomainView"));
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				save={{ domainId: "dom-1", body: { dnsResolver: "cloudflare" } }}
			/>,
		);
		await waitFor(() => expect(serverCalls(api)).toBe(1));
		await user.click(screen.getByRole("button", { name: "Save" }));
		expect(
			await screen.findByText("Domain settings saved"),
		).toBeInTheDocument();
		expect(
			screen.getByText("Staged until the next deploy."),
		).toBeInTheDocument();
		const patches = api.calls.filter((c) => c.method === "PATCH");
		expect(patches).toHaveLength(1);
		expect(patches[0]?.path).toBe("/domains/dom-1");
		expect(patches[0]?.body).toEqual({ dnsResolver: "cloudflare" });
		expect(ifMatch).toEqual(['"4"']);
		// The save invalidates routes: the on-screen server list is read again.
		await waitFor(() => expect(serverCalls(api)).toBeGreaterThan(1));
		expect(api.calls.some((c) => c.method === "PUT")).toBe(false);
		expect(api.unmatched).toEqual([]);
	});

	test("sends no PATCH for an empty body, only the auth users", async () => {
		const api = stubApi({
			"GET /servers": servers,
			"PUT /domains/:domainId/auth-users": {
				body: make("AuthUsersOutputBody"),
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				save={{
					domainId: "dom-1",
					body: {},
					users: [{ username: "ada", password: "secret" }],
				}}
			/>,
		);
		await user.click(screen.getByRole("button", { name: "Save" }));
		expect(await screen.findByText("saved")).toBeInTheDocument();
		const writes = api.calls.filter((c) => c.method !== "GET");
		expect(writes.map((c) => `${c.method} ${c.path}`)).toEqual([
			"PUT /domains/dom-1/auth-users",
		]);
		expect(writes[0]?.body).toEqual([{ username: "ada", password: "secret" }]);
	});

	test("PATCHes first, then PUTs the users, with the stale generation", async () => {
		const ifMatch: (string | null)[] = [];
		const api = stubApi({
			"GET /servers": servers,
			"PATCH /domains/:domainId": (request) => {
				ifMatch.push(request.headers.get("If-Match"));
				return Response.json(make("DomainView"));
			},
			"PUT /domains/:domainId/auth-users": {
				body: make("AuthUsersOutputBody"),
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				save={{
					domainId: "dom-1",
					body: { options: {} },
					users: [],
				}}
			/>,
		);
		await user.click(screen.getByRole("button", { name: "Save" }));
		expect(await screen.findByText("saved")).toBeInTheDocument();
		const writes = api.calls.filter((c) => c.method !== "GET");
		expect(writes.map((c) => `${c.method} ${c.path}`)).toEqual([
			"PATCH /domains/dom-1",
			"PUT /domains/dom-1/auth-users",
		]);
		expect(writes[1]?.body).toEqual([]);
		expect(ifMatch).toEqual(['"4"']);
		// No service read in this order: the PATCH goes first.
		expect(api.calls.some((c) => c.path === "/services/svc-1")).toBe(false);
	});

	test("usersFirst PUTs the users, rereads the service and PATCHes with its new generation", async () => {
		const ifMatch: (string | null)[] = [];
		const order: string[] = [];
		const api = stubApi({
			"GET /servers": servers,
			"PUT /domains/:domainId/auth-users": () => {
				order.push("put");
				return Response.json(make("DomainView"));
			},
			"GET /services/:serviceId": () => {
				order.push("get");
				return Response.json({ generation: 9 });
			},
			"PATCH /domains/:domainId": (request) => {
				order.push("patch");
				ifMatch.push(request.headers.get("If-Match"));
				return Response.json(make("DomainView"));
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				save={{
					domainId: "dom-1",
					body: { options: {} },
					users: [{ username: "ada", password: "pw" }],
					usersFirst: true,
				}}
			/>,
		);
		await user.click(screen.getByRole("button", { name: "Save" }));
		expect(await screen.findByText("saved")).toBeInTheDocument();
		expect(order).toEqual(["put", "get", "patch"]);
		expect(ifMatch).toEqual(['"9"']);
		expect(api.unmatched).toEqual([]);
	});

	test("a failed PATCH skips the users, surfaces the API message and refreshes the routes", async () => {
		const api = stubApi({
			"GET /servers": servers,
			"PATCH /domains/:domainId": problem(422, {
				detail: "redirect target is not a hostname",
				code: "invalid_redirect",
			}),
			"PUT /domains/:domainId/auth-users": {
				body: make("AuthUsersOutputBody"),
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				save={{
					domainId: "dom-1",
					body: { redirectTo: "nope" },
					users: [{ username: "ada", password: "pw" }],
				}}
			/>,
		);
		await waitFor(() => expect(serverCalls(api)).toBe(1));
		await user.click(screen.getByRole("button", { name: "Save" }));
		expect(
			await screen.findByText("failed: redirect target is not a hostname"),
		).toBeInTheDocument();
		expect(screen.queryByText("Domain settings saved")).not.toBeInTheDocument();
		expect(api.calls.some((c) => c.method === "PUT")).toBe(false);
		await waitFor(() => expect(serverCalls(api)).toBeGreaterThan(1));
	});

	test("a failed users PUT in usersFirst order never reaches the PATCH", async () => {
		const api = stubApi({
			"GET /servers": servers,
			"PUT /domains/:domainId/auth-users": problem(422, {
				detail: "password too short",
				code: "invalid_auth_user",
			}),
			"PATCH /domains/:domainId": { body: make("DomainView") },
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				save={{
					domainId: "dom-1",
					body: { options: {} },
					users: [{ username: "ada", password: "x" }],
					usersFirst: true,
				}}
			/>,
		);
		await user.click(screen.getByRole("button", { name: "Save" }));
		expect(
			await screen.findByText("failed: password too short"),
		).toBeInTheDocument();
		expect(api.calls.some((c) => c.method === "PATCH")).toBe(false);
	});

	test("a generation conflict (412) does not invalidate; the global handler owns it", async () => {
		const api = stubApi({
			"GET /servers": servers,
			"PATCH /domains/:domainId": problem(412, {
				detail: "The service changed",
				code: "generation_conflict",
			}),
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness save={{ domainId: "dom-1", body: { options: {} } }} />,
		);
		await waitFor(() => expect(serverCalls(api)).toBe(1));
		await user.click(screen.getByRole("button", { name: "Save" }));
		expect(
			await screen.findByText("failed: The service changed"),
		).toBeInTheDocument();
		// Let any (unwanted) invalidation settle before asserting there was none.
		await waitFor(() =>
			expect(api.calls.some((c) => c.method === "PATCH")).toBe(true),
		);
		expect(serverCalls(api)).toBe(1);
	});
});
