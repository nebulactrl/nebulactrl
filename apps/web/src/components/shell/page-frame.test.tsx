import { describe, expect, test } from "bun:test";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { EnvironmentPage, PageFrame } from "./page-frame";
import { renderAt, shellApi } from "./shell-test-kit";

describe("PageFrame", () => {
	test("is a page named by data-page, with its title as the one h1", () => {
		const { container } = render(
			<PageFrame page="deployments" title="Deployments">
				<p>body</p>
			</PageFrame>,
		);
		expect(container.querySelector('[data-page="deployments"]')).not.toBeNull();
		expect(
			screen.getByRole("heading", { level: 1, name: "Deployments" }),
		).toBeInTheDocument();
		expect(screen.getByText("body")).toBeInTheDocument();
	});

	test("the title is the shared element a sidebar label morphs into", () => {
		render(
			<PageFrame page="deployments" title="Deployments">
				x
			</PageFrame>,
		);
		expect(screen.getByRole("heading", { level: 1 })).toHaveAttribute(
			"data-vt",
			"nav:deployments",
		);
	});

	test("an entity's own page passes the morph to its title row instead", () => {
		const { container } = render(
			<PageFrame
				page="server"
				title="agent1"
				morph={{ kind: "server", id: "s1" }}
			>
				x
			</PageFrame>,
		);
		expect(screen.getByRole("heading", { level: 1 })).not.toHaveAttribute(
			"data-vt",
		);
		const anchor = container.querySelector("[data-vt-anchor]");
		expect(anchor).toHaveAttribute("data-vt", "server:s1");
		expect(anchor).toContainElement(screen.getByRole("heading", { level: 1 }));
	});

	test("shows the description and the actions beside the title", () => {
		render(
			<PageFrame
				page="servers"
				title="Servers"
				description="Hosts running your services"
				actions={<button type="button">Add server</button>}
			>
				x
			</PageFrame>,
		);
		expect(screen.getByText("Hosts running your services")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Add server" }),
		).toBeInTheDocument();
	});

	test("without a description there is no description line", () => {
		const { container } = render(
			<PageFrame page="servers" title="Servers">
				x
			</PageFrame>,
		);
		expect(container.querySelector("p")).toBeNull();
	});
});

const PROD = {
	id: "e-prod",
	projectId: "p1",
	slug: "production",
	name: "Production",
	kind: "standard",
	isProduction: true,
	parentEnvironmentId: null,
	prNumber: null,
	prTitle: "",
	createdAt: "2026-01-01T00:00:00Z",
};

function page(props: Partial<Parameters<typeof EnvironmentPage>[0]> = {}) {
	return (
		<EnvironmentPage
			projectId="p1"
			envSlug="production"
			page="deployments"
			title="Deployments"
			{...props}
		>
			{(environment) => <p>Showing {environment.name}</p>}
		</EnvironmentPage>
	);
}

describe("EnvironmentPage", () => {
	test("loads the environment, then renders the page for it", async () => {
		shellApi({ "GET /projects/:id/environments": { body: { items: [PROD] } } });
		await renderAt(page());
		expect(
			screen.getByRole("status", { name: "Loading deployments" }),
		).toBeInTheDocument();
		expect(await screen.findByText("Showing Production")).toBeInTheDocument();
		expect(
			screen.queryByRole("status", { name: "Loading deployments" }),
		).toBeNull();
	});

	test("sets the tab title from the page and environment", async () => {
		shellApi({ "GET /projects/:id/environments": { body: { items: [PROD] } } });
		await renderAt(page());
		await waitFor(() =>
			expect(document.title).toBe("Deployments · Production · NebulaCtrl"),
		);
	});

	test("hands the environment to the description and actions", async () => {
		shellApi({ "GET /projects/:id/environments": { body: { items: [PROD] } } });
		await renderAt(
			page({
				description: (environment) => `Releases in ${environment.slug}`,
				actions: (environment) => (
					<button type="button">Act on {environment.id}</button>
				),
			}),
		);
		expect(
			await screen.findByText("Releases in production"),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Act on e-prod" }),
		).toBeInTheDocument();
	});

	test("while loading there is no description or actions yet", async () => {
		shellApi({
			"GET /projects/:id/environments": () => new Promise<Response>(() => {}),
		});
		await renderAt(
			page({
				description: () => "never",
				actions: () => <button type="button">never</button>,
			}),
		);
		expect(screen.queryByText("never")).toBeNull();
		expect(
			screen.getByRole("heading", { level: 1, name: "Deployments" }),
		).toBeInTheDocument();
	});

	test("an environment the project does not have is said not to exist", async () => {
		shellApi({ "GET /projects/:id/environments": { body: { items: [PROD] } } });
		await renderAt(page({ envSlug: "ghost" }));
		expect(
			await screen.findByText("Environment not found"),
		).toBeInTheDocument();
		expect(
			screen.getByText("This project has no environment called “ghost”."),
		).toBeInTheDocument();
		expect(screen.queryByText(/Showing/)).toBeNull();
	});

	test("a failed load says so and offers a retry that asks again", async () => {
		let calls = 0;
		shellApi({
			"GET /projects/:id/environments": () => {
				calls++;
				return calls === 1
					? Response.json(
							{ title: "Control plane down", status: 503 },
							{ status: 503 },
						)
					: Response.json({ items: [PROD] });
			},
		});
		const user = userEvent.setup();
		await renderAt(page());
		expect(
			await screen.findByText("Couldn't load this project"),
		).toBeInTheDocument();
		expect(screen.getByRole("alert")).toHaveTextContent(
			/Nothing was changed; check that the control plane is running/,
		);
		await user.click(screen.getByRole("button", { name: /retry|try again/i }));
		expect(await screen.findByText("Showing Production")).toBeInTheDocument();
		expect(calls).toBe(2);
	});

	test("asks for this project's environments", async () => {
		const stub = shellApi({
			"GET /projects/:id/environments": { body: { items: [PROD] } },
		});
		await renderAt(page({ projectId: "p2" }));
		await screen.findByText("Showing Production");
		expect(stub.calls.map((c) => c.path)).toEqual([
			"/projects/p2/environments",
		]);
	});
});
