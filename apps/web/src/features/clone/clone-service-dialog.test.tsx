import { describe, expect, it } from "bun:test";
import { useLocation } from "@tanstack/react-router";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { CloneServiceDialog } from "./clone-service-dialog";
import {
	cloneResult,
	domainView,
	environment,
	server,
	service,
	serviceView,
} from "./test-fixtures";

const production = environment();
const staging = environment({
	id: "env-staging",
	name: "Staging",
	slug: "staging",
	isProduction: false,
});
const preview = environment({
	id: "env-pr-7",
	name: "PR 7",
	slug: "pr-7",
	kind: "preview",
	isProduction: false,
});

function Probe() {
	const location = useLocation();
	return (
		<output data-testid="location">
			{location.pathname}
			{location.searchStr}
		</output>
	);
}

function open(
	props: {
		source?: ReturnType<typeof service>;
		onOpenChange?: (open: boolean) => void;
		open?: boolean;
	} = {},
) {
	const onOpenChange = props.onOpenChange ?? (() => {});
	return renderWithProviders(
		<>
			<CloneServiceDialog
				service={props.source ?? service()}
				open={props.open ?? true}
				onOpenChange={onOpenChange}
			/>
			<Probe />
		</>,
		// A toast is cleared when the reader leaves the section it was raised in.
		{ path: "/p/proj-1/production" },
	);
}

const baseRoutes = {
	"GET /projects/:id/environments": {
		body: { items: [production, staging, preview] },
	},
	"GET /servers": {
		body: make("PageServer", {
			items: [
				server(),
				server({ id: "srv-2", name: "node-2", status: "offline" }),
				server({ id: "srv-3", name: "node-3", drainState: "draining" }),
			],
		}),
	},
} as const;

async function pick(
	user: ReturnType<typeof userEvent.setup>,
	label: string,
	option: string | RegExp,
) {
	await user.click(await screen.findByRole("combobox", { name: label }));
	await user.click(await screen.findByRole("option", { name: option }));
}

describe("CloneServiceDialog", () => {
	it("renders nothing while closed and fetches no environments", async () => {
		const api = stubApi({ ...baseRoutes });
		await open({ open: false });
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(
			api.calls.filter((call) => call.path.endsWith("/environments")),
		).toEqual([]);
	});

	it("checks the defaults as a dry run and shows the plan", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...baseRoutes,
			"POST /services/:id/clone": {
				body: cloneResult({
					warnings: ["Volumes start empty"],
					links: 2,
				}),
			},
		});
		await open();
		expect(
			await screen.findByRole("heading", { name: "Clone api" }),
		).toBeVisible();
		expect(screen.getByLabelText("Name")).toHaveAttribute(
			"placeholder",
			"api copy",
		);
		await user.click(screen.getByRole("button", { name: "Check" }));
		expect(await screen.findByText("Volumes start empty")).toBeVisible();
		expect(screen.getByText("2 link(s) copied.")).toBeVisible();
		const post = api.calls.filter((call) => call.method === "POST");
		expect(post).toHaveLength(1);
		expect(post[0]?.path).toBe("/services/svc-api/clone");
		expect(post[0]?.body).toEqual({ domains: "drop", dryRun: true });
		expect(api.unmatched).toEqual([]);
	});

	it("clones with the chosen name, server and domain suffix, then closes and opens the clone", async () => {
		const user = userEvent.setup();
		const requests: Request[] = [];
		const api = stubApi({
			...baseRoutes,
			"POST /services/:id/clone": (request) => {
				requests.push(request);
				return Response.json(
					cloneResult({
						dryRun: false,
						service: serviceView({ id: "svc-copy" }),
					}),
				);
			},
		});
		const closed: boolean[] = [];
		await open({
			source: service({ domains: [domainView("app.example.com")] }),
			onOpenChange: (value) => closed.push(value),
		});
		await user.type(await screen.findByLabelText("Name"), "  api beta ");
		await pick(user, "Server", /node-2/);
		await user.click(screen.getByRole("button", { name: "Suffix the names" }));
		expect(screen.getByText("app-copy.example.com")).toBeVisible();
		await user.type(screen.getByLabelText("Domains"), "Beta");
		await user.click(screen.getByRole("button", { name: "Clone service" }));
		await waitFor(() => expect(closed).toEqual([false]));
		expect(api.calls.find((call) => call.method === "POST")?.body).toEqual({
			name: "api beta",
			serverId: "srv-2",
			domains: "suffix",
			domainSuffix: "-beta",
		});
		expect(requests[0]?.headers.get("Idempotency-Key")).toMatch(
			/^[0-9a-f-]{36}$/,
		);
		expect(await screen.findByText("Cloned api")).toBeVisible();
		expect(
			screen.getByText("Staged: deploy the clone to start it."),
		).toBeVisible();
		// Same environment: the target is the source's own, so the canvas opens on the clone.
		expect(screen.getByTestId("location")).toHaveTextContent(
			"/p/proj-1/production?service=svc-copy",
		);
	});

	it("sends the environment to clone into, and offers no preview environment", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...baseRoutes,
			"POST /services/:id/clone": {
				body: cloneResult({
					dryRun: false,
					service: serviceView({ id: "c1" }),
				}),
			},
		});
		await open();
		await user.click(
			await screen.findByRole("combobox", { name: "Environment" }),
		);
		expect(
			await screen.findByRole("option", { name: /Staging/ }),
		).toBeVisible();
		expect(
			screen.queryByRole("option", { name: /PR 7/ }),
		).not.toBeInTheDocument();
		expect(
			screen.getByRole("option", { name: /Production/ }),
		).toHaveTextContent("production");
		await user.click(screen.getByRole("option", { name: /Staging/ }));
		expect(screen.getByLabelText("Name")).toHaveAttribute("placeholder", "api");
		expect(
			screen.getByText(/variable references are checked in Staging/),
		).toBeVisible();
		await user.click(screen.getByRole("button", { name: "Clone service" }));
		await waitFor(() =>
			expect(screen.getByTestId("location")).toHaveTextContent(
				"/p/proj-1/staging?service=c1",
			),
		);
		expect(api.calls.find((call) => call.method === "POST")?.body).toEqual({
			environmentId: "env-staging",
			domains: "drop",
		});
	});

	it("only offers servers that can take workloads", async () => {
		const user = userEvent.setup();
		stubApi({ ...baseRoutes });
		await open();
		await user.click(await screen.findByRole("combobox", { name: "Server" }));
		expect(
			await screen.findByRole("option", { name: "Same as api" }),
		).toBeVisible();
		expect(screen.getByRole("option", { name: "node-1" })).toBeVisible();
		expect(screen.getByRole("option", { name: /node-2/ })).toHaveTextContent(
			"offline",
		);
		expect(
			screen.queryByRole("option", { name: /node-3/ }),
		).not.toBeInTheDocument();
	});

	it("blocks cloning while the plan has a hostname conflict, until a choice changes", async () => {
		const user = userEvent.setup();
		stubApi({
			...baseRoutes,
			"POST /services/:id/clone": {
				body: cloneResult({
					domains: [
						{
							service: "api",
							hostname: "app.example.com",
							action: "conflict",
							to: "app-copy.example.com",
						},
					],
				}),
			},
		});
		await open({
			source: service({ domains: [domainView("app.example.com")] }),
		});
		await user.click(
			await screen.findByRole("button", { name: "Suffix the names" }),
		);
		await user.click(screen.getByRole("button", { name: "Check" }));
		expect(
			await screen.findByText("A hostname is already in use"),
		).toBeVisible();
		expect(
			screen.getByRole("button", { name: "Clone service" }),
		).toBeDisabled();
		await user.type(screen.getByLabelText("Domains"), "x");
		expect(
			screen.queryByText("A hostname is already in use"),
		).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Clone service" })).toBeEnabled();
	});

	it("explains a domain_conflict refusal in words", async () => {
		const user = userEvent.setup();
		stubApi({
			...baseRoutes,
			"POST /services/:id/clone": problem(409, {
				code: "domain_conflict",
				title: "Conflict",
				status: 409,
			}),
		});
		const closed: boolean[] = [];
		await open({ onOpenChange: (v) => closed.push(v) });
		await user.click(
			await screen.findByRole("button", { name: "Clone service" }),
		);
		expect(
			await screen.findByText(
				"A hostname of the clone is already in use. Choose another suffix, or clone without domains.",
			),
		).toBeVisible();
		expect(closed).toEqual([]);
	});

	it("shows the server's own message for any other refusal", async () => {
		const user = userEvent.setup();
		stubApi({
			...baseRoutes,
			"POST /services/:id/clone": problem(422, {
				code: "not_applicable",
				detail: "Databases cannot be cloned here",
			}),
		});
		await open();
		await user.click(await screen.findByRole("button", { name: "Check" }));
		expect(
			await screen.findByText("Databases cannot be cloned here"),
		).toBeVisible();
	});

	it("uses a new idempotency key once the choices change", async () => {
		const user = userEvent.setup();
		const keys: string[] = [];
		stubApi({
			...baseRoutes,
			"POST /services/:id/clone": (request) => {
				keys.push(request.headers.get("Idempotency-Key") ?? "");
				return Response.json({}, { status: 500 });
			},
		});
		await open();
		await user.click(
			await screen.findByRole("button", { name: "Clone service" }),
		);
		expect(
			await screen.findByText("Request failed with status 500."),
		).toBeVisible();
		await waitFor(() => expect(keys).toHaveLength(1));
		await user.type(screen.getByLabelText("Name"), "x");
		await user.click(screen.getByRole("button", { name: "Clone service" }));
		await waitFor(() => expect(keys).toHaveLength(2));
		expect(keys[0]).not.toBe(keys[1]);
	});

	it("dismisses with Cancel", async () => {
		const user = userEvent.setup();
		stubApi({ ...baseRoutes });
		const closed: boolean[] = [];
		await open({ onOpenChange: (v) => closed.push(v) });
		await user.click(await screen.findByRole("button", { name: "Cancel" }));
		expect(closed).toEqual([false]);
	});
});
