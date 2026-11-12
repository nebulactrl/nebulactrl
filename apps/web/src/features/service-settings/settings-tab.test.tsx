import { describe, expect, setDefaultTimeout, test } from "bun:test";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { Service } from "#/api/models";
import { makeService } from "#/features/canvas/lib/fixtures";
import { type ApiReply, type ApiRoutes, stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import type { CategoryId } from "./settings-nav-model";
import { SettingsTab } from "./settings-tab";

// The tab mounts every section of every category, so a render is slow in happy-dom.
setDefaultTimeout(30_000);

/** Replaces a field's text at once (typing key by key re-renders the whole tab each time). */
function enter(field: HTMLElement, value: string) {
	fireEvent.change(field, { target: { value } });
}

/** The queries the other sections of the tab issue on mount; none is under test here. */
const children = (id: string): ApiRoutes => ({
	[`GET /services/${id}/config-source`]: problem(404),
	"GET /instance/docker-options": problem(500),
	"GET /servers": { body: { items: [] } },
	[`GET /services/${id}/deploy-hook`]: problem(404),
	[`POST /services/${id}/deployments/plan`]: problem(404),
	"GET /integrations/cloudflare": problem(500),
	"GET /environments/:environmentId/services": { body: { items: [] } },
	"GET /networks": { body: { items: [] } },
	"GET /me": problem(500),
	[`GET /services/${id}/mesh`]: problem(404),
	[`GET /services/${id}/retired-volumes`]: { body: { items: [] } },
	[`GET /services/${id}/volumes`]: { body: { items: [] } },
	[`GET /services/${id}/uptime-monitor`]: problem(404),
	[`GET /services/${id}/tasks`]: { body: { items: [] } },
	[`GET /services/${id}/firewall`]: problem(404),
});

/** Mounts the tab with its own `?section=` state, recording every change the tab asks for. */
function Host({
	service,
	initial,
	changes,
}: {
	service: Service;
	initial?: CategoryId | undefined;
	changes: CategoryId[];
}) {
	const [section, setSection] = useState<string | undefined>(initial);
	return (
		<SettingsTab
			service={service}
			section={section}
			onSectionChange={(next) => {
				changes.push(next);
				setSection(next);
			}}
		/>
	);
}

async function mount(
	service: Service,
	routes: ApiRoutes = {},
	initial?: CategoryId,
) {
	const api = stubApi({ ...children(service.id), ...routes });
	const changes: CategoryId[] = [];
	const view = await renderWithProviders(
		<Host service={service} initial={initial} changes={changes} />,
	);
	return { api, changes, ...view };
}

const saveButton = () => screen.getByRole("button", { name: "Save changes" });

const patchCalls = (api: ReturnType<typeof stubApi>) =>
	api.calls.filter((call) => call.method === "PATCH");

const tab = (name: string) =>
	screen.getByRole("tab", { name: new RegExp(`^${name}`) });

describe("SettingsTab saving", () => {
	test("editing the name stages Save; saving PATCHes the minimal body with If-Match and resets the bar", async () => {
		const user = userEvent.setup();
		const service = makeService({ id: "s1", name: "api", generation: 7 });
		const { api } = await mount(service, {
			"PATCH /services/s1": { body: service },
		});
		expect(await screen.findByText("All changes saved")).toBeInTheDocument();
		expect(saveButton()).toBeDisabled();

		const name = screen.getByLabelText("Name");
		enter(name, "api-v2");

		expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
		expect(saveButton()).toBeEnabled();
		expect(within(tab("General")).getByText("edited")).toBeInTheDocument();

		await user.click(saveButton());

		expect(await screen.findByText("Settings saved")).toBeInTheDocument();
		expect(
			screen.getByText("Staged until the next deploy."),
		).toBeInTheDocument();
		const patches = patchCalls(api);
		expect(patches).toHaveLength(1);
		expect(patches[0]?.path).toBe("/services/s1");
		expect(patches[0]?.body).toEqual({ name: "api-v2" });
		// The bar resets and the form follows the saved service again.
		expect(await screen.findByText("All changes saved")).toBeInTheDocument();
		expect(screen.getByLabelText("Name")).toHaveValue("api");
		expect(within(tab("General")).queryByText("edited")).toBeNull();
		expect(api.unmatched).toEqual([]);
	});

	test("the PATCH carries If-Match for the generation the form was opened on", async () => {
		const user = userEvent.setup();
		const service = makeService({ id: "s1", name: "api", generation: 7 });
		const seen: { ifMatch?: string | null } = {};
		await mount(service, {
			"PATCH /services/s1": (request) => {
				seen.ifMatch = request.headers.get("If-Match");
				return Response.json(service);
			},
		});
		enter(await screen.findByLabelText("Name"), "apix");
		await user.click(saveButton());
		await screen.findByText("Settings saved");
		expect(seen.ifMatch).toBe('"7"');
	});

	test("an edit in another category sends only that field, under config", async () => {
		const user = userEvent.setup();
		const service = makeService({ id: "s1" });
		const { api } = await mount(service, {
			"PATCH /services/s1": { body: service },
		});
		await user.click(tab("Deploy"));
		const grace = screen.getByLabelText("Stop grace period (s)");
		enter(grace, "45");
		expect(within(tab("Deploy")).getByText("edited")).toBeInTheDocument();
		await user.click(saveButton());
		await screen.findByText("Settings saved");
		const body = patchCalls(api)[0]?.body as {
			name?: string;
			config?: { stopGraceSeconds?: number; image?: string };
		};
		expect(body.name).toBeUndefined();
		expect(body.config?.stopGraceSeconds).toBe(45);
	});

	test("Discard drops the edits and hides nothing else", async () => {
		const user = userEvent.setup();
		const service = makeService({ id: "s1", name: "api" });
		const { api } = await mount(service);
		const name = await screen.findByLabelText("Name");
		enter(name, "api-copy");
		expect(name).toHaveValue("api-copy");
		await user.click(screen.getByRole("button", { name: "Discard" }));
		expect(screen.getByLabelText("Name")).toHaveValue("api");
		expect(screen.getByText("All changes saved")).toBeInTheDocument();
		expect(patchCalls(api)).toEqual([]);
	});

	test("an invalid draft disables Save and points at the field", async () => {
		const user = userEvent.setup();
		const service = makeService({ id: "s1", name: "api" });
		const { api } = await mount(service);
		enter(await screen.findByLabelText("Name"), "");

		expect(
			screen.getByText("Fix the highlighted fields to save."),
		).toBeInTheDocument();
		expect(saveButton()).toBeDisabled();
		expect(screen.getByText("Name is required.")).toBeInTheDocument();
		expect(screen.getByLabelText("Name")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(within(tab("General")).getByText("needs fix")).toBeInTheDocument();
		// Discard is still on offer, and nothing is sent.
		expect(screen.getByRole("button", { name: "Discard" })).toBeEnabled();
		await user.click(saveButton());
		expect(patchCalls(api)).toEqual([]);
	});

	test("from another category an invalid field offers 'Show in <category>'", async () => {
		const user = userEvent.setup();
		const service = makeService({ id: "s1", name: "api" });
		const { changes } = await mount(service);
		enter(await screen.findByLabelText("Name"), "");
		// On the problem's own category there is nothing to jump to.
		expect(screen.queryByRole("button", { name: /^Show in/ })).toBeNull();

		await user.click(tab("Deploy"));
		const show = screen.getByRole("button", { name: "Show in General" });
		await user.click(show);

		expect(changes).toEqual(["deploy", "general"]);
		expect(tab("General")).toHaveAttribute("aria-selected", "true");
		expect(screen.queryByRole("button", { name: /^Show in/ })).toBeNull();
	});

	test("fixing the field clears the invalid state", async () => {
		const service = makeService({ id: "s1", name: "api" });
		await mount(service);
		const name = await screen.findByLabelText("Name");
		enter(name, "");
		enter(name, "ok");
		expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
		expect(screen.queryByText("Name is required.")).toBeNull();
		expect(within(tab("General")).queryByText("needs fix")).toBeNull();
		expect(saveButton()).toBeEnabled();
	});
});

describe("SettingsTab failures", () => {
	test("generation_conflict resets the draft to the service and shows no error", async () => {
		const user = userEvent.setup();
		const service = makeService({ id: "s1", name: "api" });
		const { api, changes } = await mount(service, {
			"PATCH /services/s1": problem(412, {
				code: "generation_conflict",
				detail: "stale",
			}),
		});
		enter(await screen.findByLabelText("Name"), "api-mine");
		await user.click(saveButton());

		await waitFor(() =>
			expect(screen.getByLabelText("Name")).toHaveValue("api"),
		);
		expect(patchCalls(api)).toHaveLength(1);
		expect(screen.getByText("All changes saved")).toBeInTheDocument();
		expect(screen.queryByText("stale")).toBeNull();
		expect(changes).toEqual([]);
		expect(screen.queryByText("Settings saved")).toBeNull();
	});

	test("process_in_use lands on Deploy and the message stays in the bar", async () => {
		const user = userEvent.setup();
		const service = makeService({ id: "s1", name: "api" });
		const { changes } = await mount(service, {
			"PATCH /services/s1": problem(409, {
				code: "process_in_use",
				detail: "Process worker still has replicas.",
			}),
		});
		enter(await screen.findByLabelText("Name"), "apix");
		await user.click(saveButton());

		expect(
			await screen.findByText("Process worker still has replicas."),
		).toBeInTheDocument();
		expect(changes).toEqual(["deploy"]);
		expect(tab("Deploy")).toHaveAttribute("aria-selected", "true");
		// The edit is kept so it can be retried, and Save stays available.
		expect(saveButton()).toBeEnabled();
		expect(screen.queryByText("All changes saved")).toBeNull();
	});

	test("a tailnet error shows its friendly copy and lands on Networking", async () => {
		const user = userEvent.setup();
		const service = makeService({ id: "s1", name: "api" });
		const { changes } = await mount(service, {
			"PATCH /services/s1": problem(422, {
				code: "tailnet_not_configured",
				detail: "raw server text",
			}),
		});
		enter(await screen.findByLabelText("Name"), "apix");
		await user.click(saveButton());

		expect(
			await screen.findByText(
				"Connect Tailscale or Headscale in Team settings → Integrations first.",
			),
		).toBeInTheDocument();
		expect(screen.queryByText("raw server text")).toBeNull();
		expect(changes).toEqual(["networking"]);
	});

	test("a Docker options error lands on Advanced", async () => {
		const user = userEvent.setup();
		const service = makeService({ id: "s1", name: "api" });
		const { changes } = await mount(service, {
			"PATCH /services/s1": problem(422, {
				code: "invalid_config",
				detail: "Docker capability SYS_BOGUS is not allowed.",
			}),
		});
		enter(await screen.findByLabelText("Name"), "apix");
		await user.click(saveButton());

		expect(
			await screen.findAllByText("Docker capability SYS_BOGUS is not allowed."),
		).not.toHaveLength(0);
		expect(changes).toEqual(["advanced"]);
		expect(tab("Advanced")).toHaveAttribute("aria-selected", "true");
	});

	test("an invalid_config error that is not about Docker stays where it is", async () => {
		const user = userEvent.setup();
		const service = makeService({ id: "s1", name: "api" });
		const { changes } = await mount(service, {
			"PATCH /services/s1": problem(422, {
				code: "invalid_config",
				detail: "Port 80 is taken.",
			}),
		});
		enter(await screen.findByLabelText("Name"), "apix");
		await user.click(saveButton());

		expect(await screen.findByText("Port 80 is taken.")).toBeInTheDocument();
		expect(changes).toEqual([]);
		expect(tab("General")).toHaveAttribute("aria-selected", "true");
	});

	test("an unexpected failure shows its message in the bar and keeps the draft", async () => {
		const user = userEvent.setup();
		const service = makeService({ id: "s1", name: "api" });
		const { changes } = await mount(service, {
			"PATCH /services/s1": problem(500, {
				code: "internal",
				detail: "The database is down.",
			}),
		});
		const name = await screen.findByLabelText("Name");
		enter(name, "apix");
		await user.click(saveButton());

		expect(
			await screen.findByText("The database is down."),
		).toBeInTheDocument();
		expect(changes).toEqual([]);
		expect(screen.getByLabelText("Name")).toHaveValue("apix");
		expect(screen.queryByText("Settings saved")).toBeNull();
	});

	test("a failed save can be retried and then succeeds", async () => {
		const user = userEvent.setup();
		const service = makeService({ id: "s1", name: "api" });
		let attempts = 0;
		await mount(service, {
			"PATCH /services/s1": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{ code: "internal", detail: "Try again later.", status: 500 },
							{ status: 500 },
						)
					: Response.json(service);
			},
		});
		enter(await screen.findByLabelText("Name"), "apix");
		await user.click(saveButton());
		await screen.findByText("Try again later.");
		await user.click(saveButton());
		expect(await screen.findByText("Settings saved")).toBeInTheDocument();
		expect(attempts).toBe(2);
		expect(screen.queryByText("Try again later.")).toBeNull();
	});
});

describe("SettingsTab navigation", () => {
	test("lists the categories of an image service in order", async () => {
		const service = makeService({ id: "s1" });
		await mount(service);
		const tabs = screen.getAllByRole("tab").map((t) => t.textContent);
		expect(tabs).toEqual([
			"General",
			"Deploy",
			"Networking",
			"Firewall",
			"Resources",
			"Storage",
			"Operations",
			"Advanced",
			"Danger zone",
		]);
		expect(tab("General")).toHaveAttribute("aria-selected", "true");
		expect(
			screen.getByRole("heading", { name: "General", level: 2 }),
		).toBeInTheDocument();
	});

	test("opens on the category or the section named by ?section=", async () => {
		const service = makeService({ id: "s1" });
		const first = await mount(service, {}, "networking");
		expect(tab("Networking")).toHaveAttribute("aria-selected", "true");
		first.unmount();
		const second = await mount(service);
		expect(tab("General")).toHaveAttribute("aria-selected", "true");
		second.unmount();
	});

	test("typed values survive switching category and back", async () => {
		const user = userEvent.setup();
		const service = makeService({ id: "s1", name: "api" });
		await mount(service);
		enter(await screen.findByLabelText("Name"), "api-new");
		await user.click(tab("Networking"));
		expect(tab("Networking")).toHaveAttribute("aria-selected", "true");
		await user.click(tab("General"));
		expect(screen.getByLabelText("Name")).toHaveValue("api-new");
		expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
	});

	test("the save bar follows an edit into any category with draft fields", async () => {
		const user = userEvent.setup();
		const service = makeService({ id: "s1", name: "api" });
		await mount(service);
		enter(await screen.findByLabelText("Name"), "apix");
		await user.click(tab("Advanced"));
		expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
		// Operations act at once, but unsaved edits are never hidden from view.
		await user.click(tab("Operations"));
		expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
		expect(saveButton()).toBeEnabled();
	});

	test("the save bar is hidden on Operations, Danger zone and Resources while nothing is edited", async () => {
		const user = userEvent.setup();
		const service = makeService({ id: "s1" });
		await mount(service);
		expect(await screen.findByText("All changes saved")).toBeInTheDocument();
		for (const name of ["Operations", "Danger zone", "Resources"]) {
			await user.click(tab(name));
			expect(tab(name)).toHaveAttribute("aria-selected", "true");
			expect(screen.queryByText("All changes saved")).toBeNull();
			expect(screen.queryByRole("button", { name: "Save changes" })).toBeNull();
		}
		await user.click(tab("Storage"));
		expect(screen.getByText("All changes saved")).toBeInTheDocument();
	});

	test("a failed save keeps the bar visible on a category that otherwise hides it", async () => {
		const user = userEvent.setup();
		const service = makeService({ id: "s1", name: "api" });
		await mount(service, {
			"PATCH /services/s1": problem(500, { code: "internal", detail: "Nope." }),
		});
		enter(await screen.findByLabelText("Name"), "apix");
		await user.click(saveButton());
		await screen.findByText("Nope.");
		// A Discard clears the edit, but the failure keeps the bar on Danger zone.
		await user.click(tab("Danger zone"));
		expect(screen.getByText("Nope.")).toBeInTheDocument();
	});

	test("turning on One-shot job removes Networking and keeps the rest", async () => {
		const user = userEvent.setup();
		const service = makeService({ id: "s1", name: "migrate" });
		const { api } = await mount(service, {
			"PATCH /services/s1": { body: service },
		});
		await user.click(
			await screen.findByRole("switch", { name: "One-shot job" }),
		);
		expect(
			screen
				.getAllByRole("tab")
				.map((t) => t.textContent?.replace(/edited|needs fix/, "")),
		).not.toContain("Networking");
		await user.click(saveButton());
		await screen.findByText("Settings saved");
		const body = patchCalls(api)[0]?.body as {
			config?: { oneshot?: boolean; port?: number };
		};
		expect(body.config?.oneshot).toBe(true);
		expect(body.config?.port).toBeUndefined();
	});
});

describe("SettingsTab staged changes", () => {
	test("staged changes from the service show as counts on their categories", async () => {
		const service = makeService({
			id: "s1",
			pendingChangeCounts: { variables: 0, settings: 3 },
		});
		const { api } = await mount(service, {
			"GET /services/s1/pending-changes": {
				body: {
					items: [
						{ field: "config.port", from: "80", to: "8080" },
						{ field: "domains.example.com", from: "", to: "added" },
						{ field: "config.startCommand", from: "", to: "node ." },
						{ field: "variables.FOO", from: "", to: "x" },
					],
				},
			},
		});
		expect(
			await within(tab("Networking")).findByLabelText("2 staged changes"),
		).toHaveTextContent("2");
		expect(
			within(tab("Deploy")).getByLabelText("1 staged change"),
		).toHaveTextContent("1");
		expect(
			within(tab("General")).queryByRole("generic", { name: /staged/ }),
		).toBeNull();
		expect(
			api.calls.some((c) => c.path === "/services/s1/pending-changes"),
		).toBe(true);
	});

	test("a service without staged settings never asks for pending changes", async () => {
		const service = makeService({ id: "s1" });
		const { api } = await mount(service);
		await screen.findByText("All changes saved");
		expect(
			api.calls.filter((c) => c.path.endsWith("/pending-changes")),
		).toEqual([]);
		expect(screen.queryByLabelText(/staged change/)).toBeNull();
	});
});

const COMPOSE = "services:\n  web:\n    image: nginx\n";

function makeStack(): Service {
	const base = makeService();
	return makeService({
		id: "stk1",
		name: "shop",
		kind: "stack",
		sourceType: "compose",
		config: {
			...base.config,
			stack: {
				compose: COMPOSE,
				components: [{ index: 0, name: "web", image: "nginx" }],
			},
		} as unknown as Service["config"],
	});
}

const stackCheck = (): ApiReply => ({
	body: make("StackCheck", {
		components: [{ index: 0, name: "web", image: "nginx" }],
		composeAvailable: true,
	}),
});

describe("SettingsTab for a stack", () => {
	test("shows the stack's categories and its compose file", async () => {
		const stack = makeStack();
		await mount(stack, {
			"POST /environments/env-1/stacks/check": stackCheck(),
		});
		expect(screen.getAllByRole("tab").map((t) => t.textContent)).toEqual([
			"General",
			"Deploy",
			"Networking",
			"Resources",
			"Operations",
			"Danger zone",
		]);
		expect(await screen.findByLabelText("docker-compose.yml")).toHaveValue(
			COMPOSE,
		);
		// A stack has no one-shot switch: its compose file declares the services.
		expect(screen.queryByRole("switch", { name: "One-shot job" })).toBeNull();
	});

	test("saving a changed wait timeout sends only the stack block", async () => {
		const user = userEvent.setup();
		const stack = makeStack();
		const { api } = await mount(stack, {
			"POST /environments/env-1/stacks/check": stackCheck(),
			"PATCH /services/stk1": { body: stack },
		});
		await user.click(tab("Deploy"));
		enter(screen.getByLabelText("Wait timeout"), "120");
		await user.click(saveButton());
		await screen.findByText("Settings saved");
		const body = patchCalls(api)[0]?.body as {
			name?: string;
			config?: { stack?: { compose?: string; waitSeconds?: number } };
		};
		expect(body.name).toBeUndefined();
		expect(body.config?.stack?.waitSeconds).toBe(120);
		expect(body.config?.stack?.compose).toBe(COMPOSE);
	});

	test("an edited compose file blocks Save until its check passes", async () => {
		const stack = makeStack();
		const { api } = await mount(stack, {
			"POST /environments/env-1/stacks/check": async (request) => {
				const text = await request.text();
				return Response.json({
					components: [{ index: 0, name: "web", image: "nginx" }],
					violations: text.includes("privileged")
						? [
								{
									service: "web",
									key: "privileged",
									message: "no",
									kind: "unsupported",
								},
							]
						: [],
					notes: [],
					unrestricted: false,
					composeAvailable: true,
				});
			},
			"PATCH /services/stk1": { body: stack },
		});
		const editor = await screen.findByLabelText("docker-compose.yml");
		enter(editor, `${COMPOSE}    privileged: true\n`);
		expect(
			await screen.findAllByText("1 problem: 1 not supported."),
		).not.toHaveLength(0);
		expect(saveButton()).toBeDisabled();
		expect(
			screen.getByText("Fix the highlighted fields to save."),
		).toBeInTheDocument();

		enter(editor, `${COMPOSE}    restart: always\n`);
		await waitFor(() => expect(saveButton()).toBeEnabled());
		expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
		expect(
			api.calls.filter((c) => c.path === "/environments/env-1/stacks/check")
				.length,
		).toBeGreaterThan(1);
	});

	test("an empty compose file is invalid", async () => {
		const stack = makeStack();
		await mount(stack, {
			"POST /environments/env-1/stacks/check": stackCheck(),
		});
		enter(await screen.findByLabelText("docker-compose.yml"), "");
		expect(
			await screen.findAllByText("A Compose file is required."),
		).not.toHaveLength(0);
		expect(saveButton()).toBeDisabled();
	});
});
