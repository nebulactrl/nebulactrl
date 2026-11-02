import { afterEach, describe, expect, it, jest, spyOn } from "bun:test";
import { useLocation } from "@tanstack/react-router";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Environment, Project } from "#/api/models";
import { serviceView } from "#/features/deploy-button/test-fixtures";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ProjectCard } from "./project-card";
import { useOpenProject } from "./use-open-project";

const PROJECT = {
	id: "p1",
	name: "Storefront",
	slug: "storefront",
	description: "The shop and its workers",
} as Project;

const env = (id: string, slug: string, name: string, isProduction = false) =>
	make("Environment", { id, slug, name, isProduction });

const ENVS = [
	env("e-stg", "staging", "staging"),
	env("e-prod", "prod", "production", true),
];

function deployed(id: string, finishedAt: string) {
	return serviceView({
		id,
		environmentId: "e-prod",
		latestDeployment: {
			id: `d-${id}`,
			status: "succeeded",
			trigger: "manual",
			strategy: "recreate",
			createdAt: "2026-10-01T00:00:00Z",
			finishedAt,
		},
	} as never);
}

function Where() {
	const pathname = useLocation({ select: (l) => l.pathname });
	return <p data-testid="where">{pathname}</p>;
}

function stubProject({
	environments = ENVS,
	services = {},
}: {
	environments?: Environment[];
	services?: Record<string, object[]>;
} = {}) {
	return stubApi({
		"GET /projects/:projectId/environments": {
			body: make("PageEnvironment", { items: environments }),
		},
		"GET /environments/:environmentId/services": (request) => {
			const id = new URL(request.url).pathname.split("/").at(-2) ?? "";
			return Response.json({ items: services[id] ?? [] });
		},
	});
}

async function renderCard(project: Project = PROJECT) {
	return renderWithProviders(
		<>
			<Where />
			<ProjectCard project={project} />
		</>,
	);
}

const link = () => screen.getByRole("link", { name: /Storefront/ });

describe("ProjectCard", () => {
	it("shows the name, slug and description", async () => {
		stubProject();
		await renderCard();
		expect(await screen.findByText("Storefront")).toBeInTheDocument();
		expect(screen.getByText("storefront")).toBeInTheDocument();
		expect(screen.getByText("The shop and its workers")).toBeInTheDocument();
	});

	it("leaves the description out when there is none", async () => {
		stubProject();
		await renderCard({ ...PROJECT, description: "" });
		await screen.findByText("Storefront");
		expect(screen.queryByText("The shop and its workers")).toBeNull();
	});

	it("lists the environments production first with their state in words", async () => {
		stubProject();
		await renderCard();
		expect(await screen.findByText("prod")).toBeInTheDocument();
		const slugs = screen
			.getAllByText(/^(prod|staging)$/)
			.map((el) => el.textContent);
		expect(slugs).toEqual(["prod", "staging"]);
		expect(screen.getAllByText("No services")).toHaveLength(2);
	});

	it("counts services across environments and shows when anything last deployed", async () => {
		stubProject({
			services: {
				"e-prod": [
					deployed("s1", new Date(Date.now() - 2 * 3_600_000).toISOString()),
					deployed("s2", new Date(Date.now() - 5 * 3_600_000).toISOString()),
				],
				"e-stg": [],
			},
		});
		await renderCard();
		expect(await screen.findByText("services")).toBeInTheDocument();
		expect(screen.getByText("2")).toBeInTheDocument();
		expect(screen.getByText("2h ago")).toBeInTheDocument();
		expect(screen.getByText("Live")).toBeInTheDocument();
	});

	it("uses the singular noun for one service", async () => {
		stubProject({
			services: { "e-prod": [deployed("s1", "2026-10-01T01:00:00Z")] },
		});
		await renderCard();
		expect(await screen.findByText("service")).toBeInTheDocument();
	});

	it("says never when nothing has deployed", async () => {
		stubProject();
		await renderCard();
		expect(await screen.findByText("never")).toBeInTheDocument();
		expect(screen.getByText("0")).toBeInTheDocument();
	});

	it("folds environments past the third into '+N more'", async () => {
		const many = [
			...ENVS,
			env("e3", "qa", "qa"),
			env("e4", "dev", "dev"),
			env("e5", "pr-1", "pr-1"),
		];
		stubProject({ environments: many });
		await renderCard();
		expect(await screen.findByText("+2 more environments")).toBeInTheDocument();
		// Production first, then by name: the last two by name are folded away.
		expect(screen.getAllByText(/^(prod|dev|pr-1)$/)).toHaveLength(3);
		expect(screen.queryByText("qa")).toBeNull();
		expect(screen.queryByText("staging")).toBeNull();
	});

	it("says '+1 more environment' in the singular", async () => {
		const four = [...ENVS, env("e3", "qa", "qa"), env("e4", "dev", "dev")];
		stubProject({ environments: four });
		await renderCard();
		expect(await screen.findByText("+1 more environment")).toBeInTheDocument();
	});

	it("links to the canvas of the production environment once the list has answered", async () => {
		stubProject();
		await renderCard();
		await screen.findByText("prod");
		expect(link()).toHaveAttribute("href", "/p/p1/prod");
	});

	it("lands on the production slug before the environments have loaded", async () => {
		stubApi({
			"GET /projects/:projectId/environments": () =>
				new Promise<Response>(() => {}),
		});
		await renderCard();
		expect(link()).toHaveAttribute("href", "/p/p1/production");
	});

	it("navigates to the canvas when the card is clicked", async () => {
		stubProject();
		const user = userEvent.setup();
		await renderCard();
		await screen.findByText("prod");
		await user.click(link());
		await waitFor(() =>
			expect(screen.getByTestId("where")).toHaveTextContent("/p/p1/prod"),
		);
	});
});

describe("ProjectCard actions", () => {
	afterEach(() => jest.restoreAllMocks());

	const actions = () =>
		screen.getByRole("button", { name: "Actions for Storefront" });

	async function openMenu() {
		const user = userEvent.setup();
		await renderCard();
		await screen.findByText("prod");
		await user.click(actions());
		return user;
	}

	it("offers Open, Settings, Copy project ID and Delete from the ⋯ button", async () => {
		stubProject();
		await openMenu();
		const items = await screen.findAllByRole("menuitem");
		expect(items.map((i) => i.textContent)).toEqual([
			"Open",
			"Settings",
			"Copy project ID",
			"Delete project…",
		]);
	});

	it("Open goes to the production canvas", async () => {
		stubProject();
		const user = await openMenu();
		await user.click(await screen.findByRole("menuitem", { name: "Open" }));
		await waitFor(() =>
			expect(screen.getByTestId("where")).toHaveTextContent("/p/p1/prod"),
		);
	});

	it("Settings goes to the project's settings", async () => {
		stubProject();
		const user = await openMenu();
		await user.click(await screen.findByRole("menuitem", { name: "Settings" }));
		await waitFor(() =>
			expect(screen.getByTestId("where")).toHaveTextContent("/p/p1/settings"),
		);
	});

	it("Copy project ID copies the id and says so", async () => {
		stubProject();
		const user = userEvent.setup();
		await renderCard();
		await screen.findByText("prod");
		const write = spyOn(navigator.clipboard, "writeText").mockResolvedValue();
		await user.click(actions());
		await user.click(
			await screen.findByRole("menuitem", { name: "Copy project ID" }),
		);
		await waitFor(() => expect(write).toHaveBeenCalledWith("p1"));
		expect(await screen.findByText("Copied")).toBeInTheDocument();
	});

	it("Delete asks for confirmation before anything is deleted", async () => {
		const api = stubProject();
		const user = await openMenu();
		await user.click(
			await screen.findByRole("menuitem", { name: /Delete project/ }),
		);
		expect(await screen.findByRole("alertdialog")).toBeInTheDocument();
		expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
	});

	it("opens the same actions on a right-click", async () => {
		stubProject();
		await renderCard();
		await screen.findByText("prod");
		fireEvent.contextMenu(link(), { clientX: 20, clientY: 20 });
		const menu = await screen.findByRole("menu", {
			name: "Actions for Storefront",
		});
		expect(
			within(menu)
				.getAllByRole("menuitem")
				.map((i) => i.textContent),
		).toEqual(["Open", "Settings", "Copy project ID", "Delete project…"]);
	});
});

function Opener({ section }: { section?: "deployments" | "variables" }) {
	const open = useOpenProject();
	return (
		<>
			<Where />
			<button type="button" onClick={() => void open("p1", section)}>
				Open
			</button>
		</>
	);
}

describe("useOpenProject", () => {
	it("opens the production environment, whichever comes first in the list", async () => {
		stubProject();
		await renderWithProviders(<Opener />);
		await userEvent.setup().click(screen.getByRole("button", { name: "Open" }));
		await waitFor(() =>
			expect(screen.getByTestId("where")).toHaveTextContent("/p/p1/prod"),
		);
	});

	it("keeps the section being read when switching projects", async () => {
		stubProject();
		await renderWithProviders(<Opener section="deployments" />);
		await userEvent.setup().click(screen.getByRole("button", { name: "Open" }));
		await waitFor(() =>
			expect(screen.getByTestId("where")).toHaveTextContent(
				"/p/p1/prod/deployments",
			),
		);
	});

	it("falls back to the production slug when the list cannot be read", async () => {
		stubApi({
			"GET /projects/:projectId/environments": problem(500, {
				code: "internal",
			}),
		});
		await renderWithProviders(<Opener section="variables" />);
		await userEvent.setup().click(screen.getByRole("button", { name: "Open" }));
		await waitFor(() =>
			expect(screen.getByTestId("where")).toHaveTextContent(
				"/p/p1/production/variables",
			),
		);
	});

	it("falls back to the production slug for a project with no environments", async () => {
		stubProject({ environments: [] });
		await renderWithProviders(<Opener />);
		await userEvent.setup().click(screen.getByRole("button", { name: "Open" }));
		await waitFor(() =>
			expect(screen.getByTestId("where")).toHaveTextContent("/p/p1/production"),
		);
	});
});
