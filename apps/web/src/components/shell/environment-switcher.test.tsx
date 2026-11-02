import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { PreviewView } from "#/api/gen/types.gen";
import { onOpenProjectConfig } from "#/features/project-config/open";
import { make, type Overrides } from "#/test/factories";
import { EnvironmentSwitcher } from "./environment-switcher";
import { environment, renderAt, shellApi, where } from "./shell-test-kit";
import { useProjectContext } from "./use-project-context";

function Host() {
	const context = useProjectContext();
	return context ? <EnvironmentSwitcher context={context} /> : null;
}

const PROD = environment("p1", "production", {
	id: "prod",
	isProduction: true,
	name: "Production",
});
const STAGING = environment("p1", "staging", { id: "stg", name: "Staging" });
const PR12 = environment("p1", "pr-12", {
	id: "pr12",
	kind: "preview",
	name: "PR #12 · fix login",
	prNumber: 12,
	parentEnvironmentId: "prod",
});
const PR3 = environment("p1", "pr-3", {
	id: "pr3",
	kind: "preview",
	name: "PR #3 · orphan",
	prNumber: 3,
	parentEnvironmentId: null,
});

function api(environments: unknown[], previews: Overrides<PreviewView>[] = []) {
	return shellApi({
		"GET /projects/:id/environments": { body: { items: environments } },
		"GET /projects/:id/previews": {
			body: make("ListOutputBody2", {
				items: previews.map((preview) => make("PreviewView", preview)),
			}),
		},
	});
}

async function openMenu(path: string, name: RegExp | string) {
	const user = userEvent.setup();
	const view = await renderAt(<Host />, { path });
	const trigger = await screen.findByRole("button", { name });
	await user.click(trigger);
	await screen.findByRole("menu");
	return { user, trigger, ...view };
}

describe("EnvironmentSwitcher trigger", () => {
	test("shows the environment's name, with a Production badge on production", async () => {
		api([PROD, STAGING]);
		await renderAt(<Host />, { path: "/p/p1/production" });
		const trigger = await screen.findByRole("button", { name: /Production/ });
		expect(within(trigger).getAllByText(/Production/)).toHaveLength(2);
	});

	test("a non-production environment has no badge", async () => {
		api([PROD, STAGING]);
		await renderAt(<Host />, { path: "/p/p1/staging" });
		const trigger = await screen.findByRole("button", { name: /Staging/ });
		expect(trigger).not.toHaveTextContent("Production");
	});

	test("before the list loads it shows the slug from the URL", async () => {
		shellApi({
			"GET /projects/:id/environments": () => new Promise<Response>(() => {}),
		});
		await renderAt(<Host />, { path: "/p/p1/staging" });
		expect(
			await screen.findByRole("button", { name: /staging/ }),
		).toBeInTheDocument();
	});

	test("a preview shows its status next to its name", async () => {
		api([PROD, PR12], [{ environmentId: "pr12", status: "deploying" }]);
		await renderAt(<Host />, { path: "/p/p1/pr-12" });
		const trigger = await screen.findByRole("button", { name: /PR #12/ });
		await waitFor(() => expect(trigger).toHaveTextContent("Deploying"));
		expect(trigger).not.toHaveTextContent("Production");
	});
});

describe("EnvironmentSwitcher menu", () => {
	test("lists each environment with its previews nested under it, then unattached previews", async () => {
		api(
			[PROD, STAGING, PR12, PR3],
			[
				{ environmentId: "pr12", status: "ready" },
				{ environmentId: "pr3", status: "paused" },
			],
		);
		await openMenu("/p/p1/staging", /Staging/);
		const items = screen
			.getAllByRole("menuitem")
			.map((item) => item.textContent ?? "");
		expect(items.slice(0, 4)).toEqual([
			expect.stringContaining("Production"),
			expect.stringContaining("PR #12 · fix login"),
			expect.stringContaining("Staging"),
			expect.stringContaining("PR #3 · orphan"),
		]);
		expect(items[1]).toContain("Ready");
		expect(screen.getByText("Environments")).toBeInTheDocument();
		expect(screen.getByText("Previews")).toBeInTheDocument();
	});

	test("marks the current environment and shows preview state in words", async () => {
		api([PROD, STAGING, PR3], [{ environmentId: "pr3", status: "paused" }]);
		await openMenu("/p/p1/staging", /Staging/);
		expect(screen.getByRole("menuitem", { name: /Staging/ })).toHaveAttribute(
			"aria-current",
			"true",
		);
		expect(
			screen.getByRole("menuitem", { name: /Production/ }),
		).not.toHaveAttribute("aria-current");
		const paused = await screen.findByRole("menuitem", { name: /PR #3/ });
		await waitFor(() => expect(paused).toHaveTextContent("paused"));
	});

	test("choosing an environment opens it", async () => {
		api([PROD, STAGING]);
		const { user, router } = await openMenu("/p/p1/staging", /Staging/);
		await user.click(screen.getByRole("menuitem", { name: /Production/ }));
		await waitFor(() => expect(where(router)).toBe("/p/p1/production"));
	});

	test("switching keeps the page being read", async () => {
		api([PROD, STAGING]);
		const { user, router } = await openMenu(
			"/p/p1/staging/observability",
			/Staging/,
		);
		await user.click(screen.getByRole("menuitem", { name: /Production/ }));
		await waitFor(() =>
			expect(where(router)).toBe("/p/p1/production/observability"),
		);
	});

	test("choosing a preview opens it", async () => {
		api([PROD, PR12]);
		const { user, router } = await openMenu(
			"/p/p1/production/deployments",
			/Production/,
		);
		await user.click(screen.getByRole("menuitem", { name: /PR #12/ }));
		await waitFor(() => expect(where(router)).toBe("/p/p1/pr-12/deployments"));
	});

	test("choosing the current environment goes nowhere", async () => {
		api([PROD, STAGING]);
		const { user, router } = await openMenu(
			"/p/p1/staging/variables",
			/Staging/,
		);
		await user.click(screen.getByRole("menuitem", { name: /Staging/ }));
		expect(where(router)).toBe("/p/p1/staging/variables");
	});

	test("Escape closes it", async () => {
		api([PROD, STAGING]);
		const { user } = await openMenu("/p/p1/staging", /Staging/);
		await user.keyboard("{Escape}");
		await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
	});

	test("New environment opens its dialog", async () => {
		api([PROD, STAGING]);
		const { user } = await openMenu("/p/p1/staging", /Staging/);
		await user.click(screen.getByRole("menuitem", { name: "New environment" }));
		expect(await screen.findByRole("dialog")).toBeInTheDocument();
	});

	test("Clone environment opens the clone dialog", async () => {
		api([PROD, STAGING]);
		const { user } = await openMenu("/p/p1/staging", /Staging/);
		await user.click(
			screen.getByRole("menuitem", { name: "Clone environment…" }),
		);
		expect(await screen.findByRole("dialog")).toBeInTheDocument();
	});

	test("the nebula.toml entries ask for the matching dialog and it opens", async () => {
		api([PROD, STAGING]);
		const asked: string[] = [];
		const off = onOpenProjectConfig((dialog) => asked.push(dialog));
		try {
			const { user } = await openMenu("/p/p1/staging", /Staging/);
			await user.click(
				screen.getByRole("menuitem", { name: "Review nebula.toml…" }),
			);
			await waitFor(() => expect(asked).toEqual(["review"]));
			expect(
				await screen.findByRole("dialog", { name: "Review nebula.toml" }),
			).toBeInTheDocument();
			await user.keyboard("{Escape}");
			await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

			await user.click(screen.getByRole("button", { name: /Staging/ }));
			await user.click(
				await screen.findByRole("menuitem", { name: "Export nebula.toml…" }),
			);
			await waitFor(() => expect(asked).toEqual(["review", "export"]));
			expect(await screen.findByRole("dialog")).toBeInTheDocument();
			await user.keyboard("{Escape}");
			await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

			await user.click(screen.getByRole("button", { name: /Staging/ }));
			await user.click(
				await screen.findByRole("menuitem", { name: "Config from git…" }),
			);
			await waitFor(() =>
				expect(asked).toEqual(["review", "export", "source"]),
			);
			expect(await screen.findByRole("dialog")).toBeInTheDocument();
		} finally {
			off();
		}
	});

	test("a preview has no clone or nebula.toml entries", async () => {
		api([PROD, PR12]);
		await openMenu("/p/p1/pr-12", /PR #12/);
		expect(
			screen.getByRole("menuitem", { name: "New environment" }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("menuitem", { name: "Clone environment…" }),
		).toBeNull();
		expect(
			screen.queryByRole("menuitem", { name: "Review nebula.toml…" }),
		).toBeNull();
	});
});
