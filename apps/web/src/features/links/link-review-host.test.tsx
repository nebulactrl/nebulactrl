import { afterEach, describe, expect, it } from "bun:test";
import type { QueryClient } from "@tanstack/react-query";
import { useLocation } from "@tanstack/react-router";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { LinkView, ServiceView } from "#/api/gen/types.gen";
import { type ApiStub, stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { LinkReviewHost } from "./link-review-host";
import { linkActions } from "./store";
import { linkView, preview, proposal, serviceView } from "./test-fixtures";

// The link store is global: an open sheet must not leak into the next test.
afterEach(() => linkActions.closeReferences());

const production = {
	id: "env-1",
	name: "Production",
	slug: "production",
	projectId: "proj-1",
	isProduction: true,
	kind: "standard",
	parentEnvironmentId: null,
	prNumber: null,
	prTitle: "",
	createdAt: "2026-01-01T00:00:00Z",
};

function Probe() {
	const location = useLocation();
	return (
		<div data-testid="location">
			{location.pathname}
			{location.searchStr}
		</div>
	);
}

interface World {
	services: ServiceView[];
	links: LinkView[];
}

function routes(world: World, extra: Record<string, never | object> = {}) {
	return {
		"GET /projects": { body: { items: [] } },
		"GET /projects/:id/environments": { body: { items: [production] } },
		"GET /environments/:id/services": () =>
			Response.json({ items: world.services }),
		"GET /environments/:id/links": () => Response.json({ items: world.links }),
		"GET /environments/:id/link-proposals": (req: Request) => {
			const from = new URL(req.url).searchParams.get("from");
			return Response.json(
				preview({ proposedVariables: [proposal(`FROM_${from}`)] }),
			);
		},
		...extra,
	};
}

/** Waits until the environment's services and links have been read into the host. */
async function settle(api: ApiStub, queryClient: QueryClient) {
	await waitFor(() => {
		expect(api.calls.some((c) => c.path.endsWith("/services"))).toBe(true);
		expect(api.calls.some((c) => c.path.endsWith("/links"))).toBe(true);
		expect(queryClient.isFetching()).toBe(0);
	});
	await act(async () => {});
}

/**
 * Waits for the sheet to leave. It polls the DOM directly: a role query per
 * tick is slow enough to starve the exit animation it is waiting for.
 */
async function dialogGone() {
	await waitFor(
		() => expect(document.querySelector('[role="dialog"]')).toBeNull(),
		{ timeout: 5000 },
	);
	expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
}

async function mount() {
	return renderWithProviders(
		<>
			<LinkReviewHost />
			<Probe />
		</>,
		{ path: "/p/proj-1/production", routePath: "/p/$projectId/$envSlug" },
	);
}

const web = serviceView("web");
const worker = serviceView("worker", { kind: "worker" });
const db = serviceView("postgres", { kind: "database" });

describe("LinkReviewHost: connecting", () => {
	it("opens the connect popover for a cable dropped from an app on a database", async () => {
		const api = stubApi(routes({ services: [web, db], links: [] }));
		const { queryClient } = await mount();
		await settle(api, queryClient);
		act(() => linkActions.request("web", "postgres", { x: 10, y: 10 }));
		expect(
			await screen.findByRole("dialog", { name: "Connect services" }),
		).toBeVisible();
		expect(await screen.findByLabelText("Stage FROM_web on web")).toBeChecked();
	});

	it("makes the database the provider whichever end the cable left from", async () => {
		const api = stubApi(routes({ services: [web, db], links: [] }));
		const { queryClient } = await mount();
		await settle(api, queryClient);
		act(() => linkActions.request("postgres", "web", { x: 10, y: 10 }));
		expect(await screen.findByLabelText("Stage FROM_web on web")).toBeVisible();
		const proposals = api.calls.filter((c) =>
			c.path.endsWith("/link-proposals"),
		);
		expect(
			proposals.map((c) => [c.query.get("from"), c.query.get("to")]),
		).toEqual([["web", "postgres"]]);
	});

	it("ignores a cable to a service it does not know", async () => {
		const api = stubApi(routes({ services: [web, db], links: [] }));
		const { queryClient } = await mount();
		await settle(api, queryClient);
		act(() => linkActions.request("web", "ghost"));
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(api.calls.some((c) => c.path.endsWith("/link-proposals"))).toBe(
			false,
		);
	});
});

describe("LinkReviewHost: database suggestion", () => {
	it("does not suggest a database that was there when the environment opened", async () => {
		const api = stubApi(routes({ services: [web, db], links: [] }));
		const { queryClient } = await mount();
		await settle(api, queryClient);
		expect(screen.queryByRole("status")).not.toBeInTheDocument();
	});

	it("offers a database that appears to the apps that have none, and connects them", async () => {
		const user = userEvent.setup();
		const world: World = { services: [web, worker], links: [] };
		const api = stubApi(routes(world));
		const { queryClient } = await mount();
		await settle(api, queryClient);
		world.services = [web, worker, db];
		await act(async () => {
			await queryClient.invalidateQueries();
		});
		const status = await screen.findByRole("status");
		expect(status).toHaveTextContent(
			"postgres is not wired in. web and worker can reach it.",
		);
		await user.click(within(status).getByRole("button", { name: "Connect" }));
		const dialog = await screen.findByRole("dialog", {
			name: "Connect services",
		});
		expect(await within(dialog).findByText("2 services")).toBeVisible();
		expect(
			api.calls
				.filter((c) => c.path.endsWith("/link-proposals"))
				.map((c) => [c.query.get("from"), c.query.get("to")]),
		).toEqual([
			["web", "postgres"],
			["worker", "postgres"],
		]);
	});

	it("leaves out apps that already reach a database, and stays quiet when none are left", async () => {
		const world: World = {
			services: [web],
			links: [linkView("l0", "web", "other-db")],
		};
		const other = serviceView("other-db", { kind: "database" });
		world.services = [web, other];
		const api = stubApi(routes(world));
		const { queryClient } = await mount();
		await settle(api, queryClient);
		world.services = [web, other, db];
		await act(async () => {
			await queryClient.invalidateQueries();
		});
		await waitFor(() =>
			expect(
				api.calls.filter((c) => c.path.endsWith("/services")).length,
			).toBeGreaterThan(1),
		);
		await act(async () => {});
		expect(screen.queryByRole("status")).not.toBeInTheDocument();
	});

	it("goes away for good when dismissed", async () => {
		const user = userEvent.setup();
		const world: World = { services: [web], links: [] };
		const api = stubApi(routes(world));
		const { queryClient } = await mount();
		await settle(api, queryClient);
		world.services = [web, db];
		await act(async () => {
			await queryClient.invalidateQueries();
		});
		await user.click(
			await screen.findByRole("button", {
				name: "Dismiss the suggestion to connect postgres",
			}),
		);
		expect(screen.queryByRole("status")).not.toBeInTheDocument();
		// Another refresh does not bring it back.
		await act(async () => {
			await queryClient.invalidateQueries();
		});
		expect(screen.queryByRole("status")).not.toBeInTheDocument();
	});
});

describe("LinkReviewHost: references of an edge", () => {
	const link = linkView("l1", "web", "postgres", {
		references: [
			{ key: "DATABASE_URL", staged: false },
			{ key: "DATABASE_HOST", staged: true },
		],
		source: "reference",
	});

	it("lists the variables that make the edge, per direction", async () => {
		const reverse = linkView("l2", "postgres", "web", { source: "reference" });
		const api = stubApi(
			routes({ services: [web, db], links: [link, reverse] }),
		);
		const { queryClient } = await mount();
		await settle(api, queryClient);
		act(() => linkActions.openReferences(link));
		expect(await screen.findByRole("dialog")).toBeVisible();
		expect(
			screen.getByText(
				"web is the consumer: the variables below hold the address of postgres.",
			),
		).toBeVisible();
		const forward = screen.getByRole("region", { name: "web to postgres" });
		const keys = within(
			within(forward).getByRole("list", {
				name: "Variables of web that reference postgres",
			}),
		)
			.getAllByRole("listitem")
			.map((li) => li.textContent);
		expect(keys).toEqual(["DATABASE_URL", "DATABASE_HOST"]);
		expect(
			within(forward).getByText(
				/This connection is drawn from those references/,
			),
		).toBeVisible();
		expect(
			within(forward).queryByRole("button", { name: "Remove manual link" }),
		).toBeNull();
		const back = screen.getByRole("region", { name: "postgres to web" });
		expect(
			within(back).getByText(/No variable of postgres references web yet/),
		).toBeVisible();
	});

	it("opens the consumer's variables from a direction", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes({ services: [web, db], links: [link] }));
		const { queryClient } = await mount();
		await settle(api, queryClient);
		act(() => linkActions.openReferences(link));
		await user.click(
			await screen.findByRole("button", { name: "Open web variables" }),
		);
		await waitFor(() =>
			expect(screen.getByTestId("location")).toHaveTextContent(
				"/p/proj-1/production?service=web&tab=variables",
			),
		);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
	});

	it("removes a manual link, and says the variables are untouched", async () => {
		const user = userEvent.setup();
		const manual = linkView("l9", "web", "postgres", {
			references: [{ key: "DATABASE_URL", staged: false }],
		});
		const world: World = { services: [web, db], links: [manual] };
		const api = stubApi(
			routes(world, {
				"DELETE /links/:id": () => {
					world.links = [];
					return new Response(null, { status: 204 });
				},
			}),
		);
		const { queryClient } = await mount();
		await settle(api, queryClient);
		act(() => linkActions.openReferences(manual));
		expect(
			await screen.findByText(
				/A manual link\. Removing it keeps the connection drawn/,
			),
		).toBeVisible();
		await user.click(
			screen.getByRole("button", { name: "Remove manual link" }),
		);
		expect(await screen.findByText("Link removed")).toBeVisible();
		expect(
			screen.getByText("The services and their variables are unchanged."),
		).toBeVisible();
		expect(api.calls.find((c) => c.method === "DELETE")?.path).toBe(
			"/links/l9",
		);
		await dialogGone();
	});

	it("explains a manual link with no variables as canvas-only", async () => {
		const manual = linkView("l9", "web", "postgres");
		const api = stubApi(routes({ services: [web, db], links: [manual] }));
		const { queryClient } = await mount();
		await settle(api, queryClient);
		act(() => linkActions.openReferences(manual));
		expect(
			await screen.findByText(/The link is drawn on the canvas only/),
		).toBeVisible();
	});

	it("reports a failed removal and keeps the sheet open", async () => {
		const user = userEvent.setup();
		const manual = linkView("l9", "web", "postgres");
		const api = stubApi(
			routes(
				{ services: [web, db], links: [manual] },
				{
					"DELETE /links/:id": () =>
						Response.json({ detail: "Link is locked" }, { status: 409 }),
				},
			),
		);
		const { queryClient } = await mount();
		await settle(api, queryClient);
		act(() => linkActions.openReferences(manual));
		await user.click(
			await screen.findByRole("button", { name: "Remove manual link" }),
		);
		expect(await screen.findByText("Could not remove the link")).toBeVisible();
		expect(screen.getByText("Link is locked")).toBeVisible();
		expect(screen.getByRole("dialog")).toBeVisible();
		expect(
			screen.getByRole("button", { name: "Remove manual link" }),
		).toBeEnabled();
	});

	it("closes when the edge disappears from the canvas", async () => {
		const world: World = { services: [web, db], links: [link] };
		const api = stubApi(routes(world));
		const { queryClient } = await mount();
		await settle(api, queryClient);
		act(() => linkActions.openReferences(link));
		await screen.findByRole("dialog");
		world.links = [];
		await act(async () => {
			await queryClient.invalidateQueries();
		});
		await dialogGone();
	});

	it("closes with the Close button", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes({ services: [web, db], links: [link] }));
		const { queryClient } = await mount();
		await settle(api, queryClient);
		act(() => linkActions.openReferences(link));
		await user.click(await screen.findByRole("button", { name: "Close" }));
		await dialogGone();
	});
});
