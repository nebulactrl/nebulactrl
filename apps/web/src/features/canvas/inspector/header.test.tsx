import { afterEach, describe, expect, spyOn, test } from "bun:test";
import {
	act,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Service } from "#/api/models";
import { COPIED_MS } from "#/components/common/copy-button";
import { Sheet, SheetContent } from "#/components/ui/sheet";
import {
	closeFiles,
	closeShell,
	useFilesTarget,
	useShellTarget,
} from "#/features/terminal/store";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { makeDeployment, makeObserved, makeService } from "../lib/fixtures";
import { InspectorHeader } from "./header";
import type { InspectorTab } from "./tab-ids";

const environment = (isProduction: boolean) => ({
	id: "env-1",
	projectId: "p1",
	name: isProduction ? "Production" : "Staging",
	slug: isProduction ? "production" : "staging",
	isProduction,
	kind: "standard",
	parentEnvironmentId: null,
	prNumber: null,
	prTitle: "",
	createdAt: "2026-01-01T00:00:00Z",
});

type Routes = Parameters<typeof stubApi>[0];

/** The deployment a deploy request is answered with. */
const queued = () =>
	make("DeploymentView", { id: "dep-2", serviceId: "svc-h", status: "queued" });

/** The reads the header makes on its own, plus whatever the test adds. */
function headerApi(extra: Routes = {}, production = false) {
	return stubApi({
		"GET /projects": { body: { items: [] } },
		"GET /projects/:projectId/environments": {
			body: { items: [environment(production)] },
		},
		"GET /services/:serviceId/config-source": {
			body: make("ServiceConfigSource", { managed: false }),
		},
		"GET /servers": { body: { items: [] } },
		...extra,
	});
}

const deployed = (overrides: Partial<Service> = {}) =>
	makeService({
		id: "svc-h",
		name: "api",
		currentDeploymentId: "dep-1",
		latestDeployment: makeDeployment("succeeded"),
		observed: makeObserved(),
		...overrides,
	});

async function renderHeader(
	service: Service,
	handlers: { onClose?: () => void; onTab?: (tab: InspectorTab) => void } = {},
) {
	return renderWithProviders(
		<>
			{/* The header's title belongs to the inspector's sheet. */}
			<Sheet open modal={false}>
				<SheetContent variant="docked" showCloseButton={false}>
					<InspectorHeader
						service={service}
						onClose={handlers.onClose ?? (() => {})}
						onTab={handlers.onTab ?? (() => {})}
					/>
				</SheetContent>
			</Sheet>
			<ShellProbe />
		</>,
		{ path: "/p/p1/staging", routePath: "/p/$projectId/$envSlug" },
	);
}

function ShellProbe() {
	const shell = useShellTarget();
	const files = useFilesTarget();
	return (
		<>
			<output aria-label="shell target">{shell ?? "none"}</output>
			<output aria-label="files target">{files ?? "none"}</output>
		</>
	);
}

/** A cheap, boolean probe: a failing DOM-node assertion prints the whole element graph. */
function menuIsOpen(): boolean {
	return document.querySelector('[role="menu"]') !== null;
}

async function openMenu(name = "api") {
	const user = userEvent.setup();
	await user.click(
		screen.getByRole("button", { name: `More actions for ${name}` }),
	);
	return { user, menu: await screen.findByRole("menu") };
}

afterEach(() => {
	closeShell();
	closeFiles();
});

describe("InspectorHeader identity", () => {
	test("names the service with its state pill and source line", async () => {
		headerApi();
		await renderHeader(
			deployed({
				config: { ...makeService().config, image: "ghcr.io/acme/api:1.2" },
			}),
		);
		expect(screen.getByRole("heading", { name: "api" })).toBeInTheDocument();
		expect(screen.getByText("Live")).toBeInTheDocument();
		expect(screen.getByText("ghcr.io/acme/api:1.2")).toBeInTheDocument();
	});

	test("a service without a source says none is set", async () => {
		headerApi();
		await renderHeader(
			makeService({ config: { ...makeService().config, image: "" } }),
		);
		expect(screen.getByText("No source set yet")).toBeInTheDocument();
	});

	test("the first domain is a link to the site, with a count of the others", async () => {
		headerApi();
		const base = makeService().config;
		await renderHeader(
			deployed({
				config: { ...base, image: "app:1" },
				domains: [
					{ hostname: "api.acme.dev", pathPrefix: "", tlsMode: "auto" },
					{ hostname: "b.acme.dev", pathPrefix: "", tlsMode: "auto" },
					{ hostname: "c.acme.dev", pathPrefix: "", tlsMode: "auto" },
				] as Service["domains"],
			}),
		);
		const links = screen.getAllByRole("link", {
			name: "Open api.acme.dev in a new tab",
		});
		expect(links[0]).toHaveAttribute("href", "https://api.acme.dev/");
		expect(screen.getByText("+2")).toBeInTheDocument();
	});

	test("with Tailnet only on, the domain stays plain text", async () => {
		headerApi();
		await renderHeader(
			deployed({
				domains: [
					{ hostname: "api.acme.dev", pathPrefix: "", tlsMode: "auto" },
				] as Service["domains"],
				tailnet: {
					hidePublic: true,
					state: "offline",
					url: "",
				} as Service["tailnet"],
			}),
		);
		expect(screen.getByText("api.acme.dev")).toBeInTheDocument();
		expect(
			screen.queryByRole("link", { name: "Open api.acme.dev in a new tab" }),
		).toBeNull();
	});

	test("a production environment is captioned", async () => {
		headerApi({}, true);
		await renderHeader(deployed());
		expect(await screen.findByText("Production")).toBeInTheDocument();
	});

	test("another environment has no caption", async () => {
		headerApi();
		await renderHeader(deployed());
		await waitFor(() => expect(screen.getByText("Live")).toBeInTheDocument());
		expect(screen.queryByText("Production")).toBeNull();
	});

	test("the close button closes the inspector", async () => {
		headerApi();
		const closed: string[] = [];
		await renderHeader(deployed(), { onClose: () => closed.push("closed") });
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Close inspector" }));
		expect(closed).toEqual(["closed"]);
	});
});

describe("InspectorHeader quick actions", () => {
	test("Redeploy starts a deployment of the current configuration", async () => {
		const api = headerApi({
			"POST /services/:serviceId/deployments": {
				status: 202,
				body: queued(),
			},
		});
		await renderHeader(deployed());
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Redeploy" }));
		await waitFor(() =>
			expect(api.calls.some((c) => c.method === "POST")).toBe(true),
		);
		const call = api.calls.find((c) => c.method === "POST");
		expect(call?.path).toBe("/services/svc-h/deployments");
		expect(call?.body).toBeUndefined();
		expect(await screen.findByText("Deploying api")).toBeInTheDocument();
	});

	test("a service that never ran offers Deploy and no restart target", async () => {
		headerApi();
		await renderHeader(makeService({ name: "api" }));
		expect(screen.getByRole("button", { name: "Deploy" })).toBeEnabled();
		expect(screen.getByRole("button", { name: "Restart" })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Stop" })).toBeDisabled();
	});

	test("a deployment already running disables Redeploy and says why", async () => {
		headerApi();
		await renderHeader(
			deployed({ latestDeployment: makeDeployment("building") }),
		);
		const button = screen.getByRole("button", { name: "Redeploy" });
		expect(button).toBeDisabled();
		expect(button).toHaveAttribute("title", "A deployment is already running");
	});

	test("with staged changes the Redeploy tooltip says they go out too", async () => {
		headerApi();
		await renderHeader(deployed({ hasStagedChanges: true }));
		expect(screen.getByRole("button", { name: "Redeploy" })).toHaveAttribute(
			"title",
			"Deploy the current configuration, with its staged changes",
		);
	});

	test("Restart restarts the container without a deployment", async () => {
		const api = headerApi({
			"POST /services/:serviceId/restart": {
				status: 202,
				body: make("ActionResult"),
			},
		});
		await renderHeader(deployed());
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Restart" }));
		await waitFor(() =>
			expect(api.calls.some((c) => c.path === "/services/svc-h/restart")).toBe(
				true,
			),
		);
		expect(await screen.findByText("Restarting api")).toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("a failed restart says so", async () => {
		headerApi({
			"POST /services/:serviceId/restart": problem(500, {
				title: "Internal",
				detail: "agent unreachable",
			}),
		});
		await renderHeader(deployed());
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Restart" }));
		expect(
			await screen.findByText("Could not restart api"),
		).toBeInTheDocument();
	});

	test("a stopped service shows Start instead of Restart, and Stop is disabled", async () => {
		headerApi();
		await renderHeader(deployed({ desiredState: "stopped" }));
		const start = screen.getByRole("button", { name: "Start" });
		expect(start).toBeEnabled();
		expect(start).toHaveAttribute("title", "Start the stopped container again");
		expect(screen.queryByRole("button", { name: "Restart" })).toBeNull();
		expect(screen.getByRole("button", { name: "Stop" })).toBeDisabled();
	});

	test("Stop asks first, and stops the service once confirmed", async () => {
		const api = headerApi({
			"POST /services/:serviceId/stop": {
				status: 202,
				body: make("ActionResult"),
			},
		});
		await renderHeader(deployed());
		const user = userEvent.setup();
		await user.click(screen.getByRole("button", { name: "Stop" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(within(dialog).getByText("Stop api?")).toBeInTheDocument();
		expect(api.calls.some((c) => c.path.endsWith("/stop"))).toBe(false);
		await user.click(
			within(dialog).getByRole("button", { name: "Stop service" }),
		);
		await waitFor(() =>
			expect(api.calls.some((c) => c.path === "/services/svc-h/stop")).toBe(
				true,
			),
		);
		expect(await screen.findByText("Stopping api")).toBeInTheDocument();
	});

	test("cancelling the Stop confirmation sends nothing", async () => {
		const api = headerApi();
		await renderHeader(deployed());
		const user = userEvent.setup();
		await user.click(screen.getByRole("button", { name: "Stop" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(api.calls.some((c) => c.method === "POST")).toBe(false);
	});

	test("a route has Deploy but no Restart or Stop", async () => {
		headerApi();
		await renderHeader(
			makeService({
				kind: "route",
				sourceType: "external",
				name: "plex",
				currentDeploymentId: "dep-1",
			}),
		);
		expect(screen.getByRole("button", { name: "Deploy" })).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Restart" })).toBeNull();
		expect(screen.queryByRole("button", { name: "Stop" })).toBeNull();
	});
});

describe("InspectorHeader menu", () => {
	test("an app lists every action", async () => {
		headerApi();
		await renderHeader(deployed());
		const { menu } = await openMenu();
		expect(
			within(menu)
				.getAllByRole("menuitem")
				.map((item) => item.textContent),
		).toEqual([
			"Scale…",
			"Open settings",
			"Open shell…",
			"Browse files…",
			"Clone…",
			"Move to environment…",
			"Copy private host",
			"Copy service ID",
		]);
	});

	test("a git service also offers a redeploy without cache", async () => {
		headerApi();
		await renderHeader(deployed({ sourceType: "git" }));
		const { menu } = await openMenu();
		expect(
			within(menu).getByRole("menuitem", { name: "Redeploy without cache…" }),
		).toBeInTheDocument();
	});

	test("a route's menu is just settings and its ID", async () => {
		headerApi();
		await renderHeader(
			makeService({
				kind: "route",
				sourceType: "external",
				name: "plex",
				currentDeploymentId: "dep-1",
			}),
		);
		const { menu } = await openMenu("plex");
		expect(
			within(menu)
				.getAllByRole("menuitem")
				.map((item) => item.textContent),
		).toEqual(["Open settings", "Copy service ID"]);
	});

	test("a stack has no scale, clone, move or private host", async () => {
		headerApi();
		await renderHeader(deployed({ kind: "stack", name: "shop" }));
		const { menu } = await openMenu("shop");
		expect(
			within(menu)
				.getAllByRole("menuitem")
				.map((item) => item.textContent),
		).toEqual([
			"Open settings",
			"Open shell…",
			"Browse files…",
			"Copy service ID",
		]);
	});

	test("a service that never ran has no shell or files", async () => {
		headerApi();
		await renderHeader(makeService({ name: "api" }));
		const { menu } = await openMenu();
		const labels = within(menu)
			.getAllByRole("menuitem")
			.map((item) => item.textContent);
		expect(labels).not.toContain("Open shell…");
		expect(labels).not.toContain("Browse files…");
	});

	test("Scale is disabled for a service that cannot scale", async () => {
		headerApi();
		await renderHeader(
			deployed({
				scaling: { ...makeService().scaling, eligible: false },
			}),
		);
		const { menu } = await openMenu();
		expect(
			within(menu).getByRole("menuitem", { name: "Scale…" }),
		).toHaveAttribute("aria-disabled", "true");
	});

	test("Open settings switches the inspector to Settings", async () => {
		headerApi();
		const tabs: InspectorTab[] = [];
		await renderHeader(deployed(), { onTab: (tab) => tabs.push(tab) });
		const { user, menu } = await openMenu();
		await user.click(
			within(menu).getByRole("menuitem", { name: "Open settings" }),
		);
		expect(tabs).toEqual(["settings"]);
	});

	test("Open shell and Browse files open the terminal panels for this service", async () => {
		headerApi();
		await renderHeader(deployed());
		const first = await openMenu();
		await first.user.click(
			within(first.menu).getByRole("menuitem", { name: "Open shell…" }),
		);
		expect(screen.getByLabelText("shell target")).toHaveTextContent("svc-h");
		expect(screen.getByLabelText("files target")).toHaveTextContent("none");
	});

	test("Browse files opens the file browser for this service only", async () => {
		headerApi();
		await renderHeader(deployed());
		const { user, menu } = await openMenu();
		await user.click(
			within(menu).getByRole("menuitem", { name: "Browse files…" }),
		);
		expect(screen.getByLabelText("files target")).toHaveTextContent("svc-h");
		expect(screen.getByLabelText("shell target")).toHaveTextContent("none");
	});

	test("Scale… opens the scale dialog", async () => {
		headerApi({
			"GET /services/:serviceId": problem(500, { title: "boom" }),
		});
		await renderHeader(deployed());
		const { user, menu } = await openMenu();
		await user.click(within(menu).getByRole("menuitem", { name: "Scale…" }));
		expect(await screen.findByText("Scale service")).toBeInTheDocument();
	});

	test("Redeploy without cache confirms, then deploys with noCache", async () => {
		const api = headerApi({
			"POST /services/:serviceId/deployments": {
				status: 202,
				body: queued(),
			},
		});
		await renderHeader(deployed({ sourceType: "git" }));
		const { user, menu } = await openMenu();
		await user.click(
			within(menu).getByRole("menuitem", { name: "Redeploy without cache…" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Redeploy api without cache?"),
		).toBeInTheDocument();
		await user.click(
			within(dialog).getByRole("button", { name: "Redeploy without cache" }),
		);
		await waitFor(() =>
			expect(api.calls.some((c) => c.method === "POST")).toBe(true),
		);
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			noCache: true,
		});
	});

	test("Clone… opens the clone dialog", async () => {
		headerApi();
		await renderHeader(deployed());
		const first = await openMenu();
		await first.user.click(
			within(first.menu).getByRole("menuitem", { name: "Clone…" }),
		);
		expect(await screen.findByText("Clone api")).toBeInTheDocument();
	});

	test("Move to environment… opens the move dialog", async () => {
		headerApi();
		await renderHeader(deployed());
		const { user, menu } = await openMenu();
		await user.click(
			within(menu).getByRole("menuitem", { name: "Move to environment…" }),
		);
		expect(await screen.findByText("Move api")).toBeInTheDocument();
	});
});

describe("InspectorHeader copy items", () => {
	// userEvent.setup() installs its own clipboard, which reads back what was written.
	const clipboardText = () => navigator.clipboard.readText();

	test("Copy service ID copies it, confirms with a toast and keeps the menu open with a check", async () => {
		headerApi();
		await renderHeader(deployed());
		const { user, menu } = await openMenu();
		await user.click(
			within(menu).getByRole("menuitem", { name: "Copy service ID" }),
		);
		expect(await screen.findByText("Copied service ID")).toBeInTheDocument();
		expect(await clipboardText()).toBe("svc-h");
		// A pointer copy holds the menu open so the check can be seen.
		expect(screen.getByRole("menu")).toBeInTheDocument();
	});

	test("Copy private host copies the service's private host", async () => {
		headerApi();
		await renderHeader(deployed({ privateHost: "api.internal" }));
		const { user, menu } = await openMenu();
		await user.click(
			within(menu).getByRole("menuitem", { name: "Copy private host" }),
		);
		expect(await screen.findByText("Copied private host")).toBeInTheDocument();
		expect(await clipboardText()).toBe("api.internal");
	});

	test("the menu closes by itself once the check has shown", async () => {
		headerApi();
		await renderHeader(deployed());
		const { user, menu } = await openMenu();
		await user.click(
			within(menu).getByRole("menuitem", { name: "Copy service ID" }),
		);
		expect(await screen.findByText("Copied service ID")).toBeInTheDocument();
		expect(screen.getByRole("menu")).toBeInTheDocument();
		// The hold lasts COPIED_MS; the wait below is that hold, not a guess.
		await waitFor(() => expect(menuIsOpen()).toBe(false), {
			timeout: COPIED_MS + 2_000,
		});
	});

	test("a keyboard copy closes the menu at once", async () => {
		headerApi();
		await renderHeader(deployed());
		const { menu } = await openMenu();
		// The last input was a key press, not a pointer.
		fireEvent.keyDown(document.body, { key: "Enter" });
		await act(async () => {
			fireEvent.click(
				within(menu).getByRole("menuitem", { name: "Copy service ID" }),
			);
		});
		await waitFor(() => expect(menuIsOpen()).toBe(false));
		expect(await clipboardText()).toBe("svc-h");
	});

	test("when the browser blocks the clipboard it says so and closes the menu", async () => {
		headerApi();
		await renderHeader(deployed());
		const { user, menu } = await openMenu();
		spyOn(navigator.clipboard, "writeText").mockRejectedValue(
			new Error("NotAllowedError"),
		);
		await user.click(
			within(menu).getByRole("menuitem", { name: "Copy service ID" }),
		);
		expect(
			await screen.findByText("Could not copy service ID"),
		).toBeInTheDocument();
		expect(
			screen.getByText("The browser blocked clipboard access."),
		).toBeInTheDocument();
		await waitFor(() => expect(menuIsOpen()).toBe(false));
	});
});
