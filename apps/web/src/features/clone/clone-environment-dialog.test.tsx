import { describe, expect, it } from "bun:test";
import { useLocation } from "@tanstack/react-router";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { environmentsQuery } from "#/api/hooks";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders, testQueryClient } from "#/test/render";
import { CloneEnvironmentDialog } from "./clone-environment-dialog";
import { cloneResult, environment, envRef, server } from "./test-fixtures";

const source = environment({ id: "env-prod", name: "Production" });

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
	onOpenChange: (open: boolean) => void = () => {},
	queryClient = testQueryClient(),
) {
	return renderWithProviders(
		<>
			<CloneEnvironmentDialog
				environment={source}
				open
				onOpenChange={onOpenChange}
			/>
			<Probe />
		</>,
		{ path: "/p/proj-1/production", queryClient },
	);
}

const routes = {
	"GET /servers": {
		body: make("PageServer", {
			items: [server(), server({ id: "srv-2", name: "node-2" })],
		}),
	},
	"GET /projects/:id/environments": { body: { items: [source] } },
};

describe("CloneEnvironmentDialog", () => {
	it("needs a name before it can check or clone", async () => {
		const user = userEvent.setup();
		stubApi(routes);
		await open();
		expect(
			await screen.findByRole("heading", { name: "Clone Production" }),
		).toBeVisible();
		expect(screen.getByRole("button", { name: "Check" })).toBeDisabled();
		expect(
			screen.getByRole("button", { name: "Clone environment" }),
		).toBeDisabled();
		await user.type(screen.getByLabelText("Name"), "   ");
		expect(screen.getByRole("button", { name: "Check" })).toBeDisabled();
		await user.type(screen.getByLabelText("Name"), "staging");
		expect(screen.getByRole("button", { name: "Check" })).toBeEnabled();
		expect(
			screen.getByRole("button", { name: "Clone environment" }),
		).toBeEnabled();
	});

	it("checks as a dry run and lists the services it would create", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...routes,
			"POST /environments/:id/clone": {
				body: cloneResult({
					services: [
						{
							id: "1",
							sourceId: "0",
							name: "Postgres",
							slug: "postgres",
							kind: "database",
							fresh: true,
						},
					],
					sharedVariables: 3,
				}),
			},
		});
		await open();
		await user.type(await screen.findByLabelText("Name"), " staging ");
		await user.click(screen.getByRole("button", { name: "Check" }));
		expect(await screen.findByText("Services (1)")).toBeVisible();
		expect(
			screen.getByText("postgres · database · new, empty database"),
		).toBeVisible();
		expect(screen.getByText("3 shared variable(s) copied.")).toBeVisible();
		const post = api.calls.filter((call) => call.method === "POST");
		expect(post).toHaveLength(1);
		expect(post[0]?.path).toBe("/environments/env-prod/clone");
		expect(post[0]?.body).toEqual({
			name: "staging",
			domains: "drop",
			dryRun: true,
		});
	});

	it("clones with the server and suffix, closes, and opens the new environment", async () => {
		const user = userEvent.setup();
		const requests: Request[] = [];
		const api = stubApi({
			...routes,
			"POST /environments/:id/clone": (request) => {
				requests.push(request);
				return Response.json(
					cloneResult({
						dryRun: false,
						environment: envRef({ slug: "qa-1", name: "QA 1" }),
					}),
				);
			},
		});
		const closed: boolean[] = [];
		const queryClient = testQueryClient();
		const envKey = environmentsQuery("proj-1").queryKey;
		queryClient.setQueryData(envKey, [source]);
		await open((value) => closed.push(value), queryClient);
		await user.type(await screen.findByLabelText("Name"), "QA 1");
		await user.click(screen.getByRole("combobox", { name: "Server" }));
		expect(
			await screen.findByRole("option", {
				name: "Each service keeps its server",
			}),
		).toBeVisible();
		await user.click(screen.getByRole("option", { name: "node-2" }));
		await user.click(screen.getByRole("button", { name: "Suffix the names" }));
		expect(screen.getByLabelText("Domains")).toHaveAttribute(
			"placeholder",
			"-<name>",
		);
		await user.type(screen.getByLabelText("Domains"), "qa");
		await user.click(screen.getByRole("button", { name: "Clone environment" }));
		await waitFor(() => expect(closed).toEqual([false]));
		expect(api.calls.find((call) => call.method === "POST")?.body).toEqual({
			name: "QA 1",
			serverId: "srv-2",
			domains: "suffix",
			domainSuffix: "-qa",
		});
		expect(requests[0]?.headers.get("Idempotency-Key")).toMatch(
			/^[0-9a-f-]{36}$/,
		);
		expect(await screen.findByText("Cloned Production")).toBeVisible();
		expect(
			screen.getByText("Staged: deploy its services to start them."),
		).toBeVisible();
		expect(screen.getByTestId("location")).toHaveTextContent("/p/proj-1/qa-1");
		// The environment list is stale so the switcher shows the new environment.
		expect(queryClient.getQueryState(envKey)?.isInvalidated).toBe(true);
	});

	it("blocks on a hostname conflict until the choices change", async () => {
		const user = userEvent.setup();
		stubApi({
			...routes,
			"POST /environments/:id/clone": {
				body: cloneResult({
					domains: [
						{
							service: "web",
							hostname: "www.example.com",
							action: "conflict",
						},
					],
				}),
			},
		});
		await open();
		await user.type(await screen.findByLabelText("Name"), "qa");
		await user.click(screen.getByRole("button", { name: "Check" }));
		expect(
			await screen.findByText("A hostname is already in use"),
		).toBeVisible();
		expect(
			screen.getByRole("button", { name: "Clone environment" }),
		).toBeDisabled();
		await user.type(screen.getByLabelText("Name"), "2");
		expect(
			screen.queryByText("A hostname is already in use"),
		).not.toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Clone environment" }),
		).toBeEnabled();
	});

	it("explains a domain_conflict refusal", async () => {
		const user = userEvent.setup();
		stubApi({
			...routes,
			"POST /environments/:id/clone": problem(409, { code: "domain_conflict" }),
		});
		const closed: boolean[] = [];
		await open((value) => closed.push(value));
		await user.type(await screen.findByLabelText("Name"), "qa");
		await user.click(screen.getByRole("button", { name: "Clone environment" }));
		expect(
			await screen.findByText(
				"A hostname of the clone is already in use. Choose another suffix, or clone without domains.",
			),
		).toBeVisible();
		expect(closed).toEqual([]);
	});

	it("shows the server's message for any other refusal", async () => {
		const user = userEvent.setup();
		stubApi({
			...routes,
			"POST /environments/:id/clone": problem(409, {
				code: "slug_taken",
				detail: "An environment named qa exists",
			}),
		});
		await open();
		await user.type(await screen.findByLabelText("Name"), "qa");
		await user.click(screen.getByRole("button", { name: "Check" }));
		expect(
			await screen.findByText("An environment named qa exists"),
		).toBeVisible();
	});

	it("dismisses with Cancel", async () => {
		const user = userEvent.setup();
		stubApi(routes);
		const closed: boolean[] = [];
		await open((value) => closed.push(value));
		await user.click(await screen.findByRole("button", { name: "Cancel" }));
		expect(closed).toEqual([false]);
	});
});
