import { describe, expect, it } from "bun:test";
import { useLocation } from "@tanstack/react-router";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { MoveServiceDialog } from "./move-service-dialog";
import { environment, moveResult, service } from "./test-fixtures";

const production = environment();
const staging = environment({
	id: "env-staging",
	name: "Staging",
	slug: "staging",
	isProduction: false,
});
const preview = environment({
	id: "env-pr",
	name: "PR 9",
	slug: "pr-9",
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

function open(onOpenChange: (open: boolean) => void = () => {}) {
	return renderWithProviders(
		<>
			<MoveServiceDialog service={service()} open onOpenChange={onOpenChange} />
			<Probe />
		</>,
		{ path: "/p/proj-1/production" },
	);
}

const environments = {
	"GET /projects/:id/environments": {
		body: { items: [production, staging, preview] },
	},
};

async function chooseStaging(user: ReturnType<typeof userEvent.setup>) {
	await user.click(await screen.findByRole("combobox", { name: "Move to" }));
	await user.click(await screen.findByRole("option", { name: /Staging/ }));
}

describe("MoveServiceDialog", () => {
	it("starts with nothing picked and Move disabled", async () => {
		stubApi(environments);
		await open();
		expect(
			await screen.findByRole("heading", { name: "Move api" }),
		).toBeVisible();
		expect(screen.getByRole("button", { name: "Move service" })).toBeDisabled();
		expect(screen.queryByText(/Nothing breaks/)).not.toBeInTheDocument();
	});

	it("offers neither the service's own environment nor a preview", async () => {
		const user = userEvent.setup();
		stubApi(environments);
		await open();
		await user.click(await screen.findByRole("combobox", { name: "Move to" }));
		expect(
			await screen.findByRole("option", { name: /Staging/ }),
		).toBeVisible();
		expect(
			screen.queryByRole("option", { name: /Production/ }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("option", { name: /PR 9/ }),
		).not.toBeInTheDocument();
	});

	it("asks for a dry-run plan when an environment is picked, then moves", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...environments,
			"POST /services/:id/move": (request) =>
				request
					.clone()
					.json()
					.then((body: { dryRun?: boolean }) =>
						Response.json(
							body.dryRun
								? moveResult()
								: moveResult({ dryRun: false, applied: true }),
						),
					),
		});
		const closed: boolean[] = [];
		await open((value) => closed.push(value));
		await chooseStaging(user);
		expect(await screen.findByText("Nothing breaks.")).toBeVisible();
		await user.click(screen.getByRole("button", { name: "Move service" }));
		await waitFor(() => expect(closed).toEqual([false]));
		const posts = api.calls.filter((call) => call.method === "POST");
		expect(posts.map((call) => [call.path, call.body])).toEqual([
			[
				"/services/svc-api/move",
				{ environmentId: "env-staging", dryRun: true },
			],
			[
				"/services/svc-api/move",
				{ environmentId: "env-staging", force: false },
			],
		]);
		expect(await screen.findByText("Moved api to Staging")).toBeVisible();
		expect(
			screen.getByText("Staged: deploy it to apply the move."),
		).toBeVisible();
		expect(screen.getByTestId("location")).toHaveTextContent(
			"/p/proj-1/staging?service=svc-api",
		);
	});

	it("requires an explicit Move anyway for a plan that breaks references, and forces it", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...environments,
			"POST /services/:id/move": (request) =>
				request
					.clone()
					.json()
					.then((body: { dryRun?: boolean }) =>
						Response.json(
							moveResult({
								blocked: true,
								dryRun: body.dryRun === true,
								outgoing: [
									{
										service: "api",
										key: "REDIS_URL",
										ref: "redis.URL",
										status: "unresolved",
										message: "no redis in Staging",
									},
								],
							}),
						),
					),
		});
		const closed: boolean[] = [];
		await open((value) => closed.push(value));
		await chooseStaging(user);
		expect(
			await screen.findByText("This move breaks one reference."),
		).toBeVisible();
		expect(
			screen.queryByRole("button", { name: "Move service" }),
		).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Move anyway" }));
		await waitFor(() => expect(closed).toEqual([false]));
		expect(
			api.calls.filter((call) => call.method === "POST").at(-1)?.body,
		).toEqual({ environmentId: "env-staging", force: true });
	});

	it("explains a move_blocked refusal and keeps the dialog open", async () => {
		const user = userEvent.setup();
		stubApi({
			...environments,
			"POST /services/:id/move": (request) =>
				request
					.clone()
					.json()
					.then((body: { dryRun?: boolean }) =>
						body.dryRun
							? Response.json(moveResult())
							: Response.json({ code: "move_blocked" }, { status: 409 }),
					),
		});
		const closed: boolean[] = [];
		await open((value) => closed.push(value));
		await chooseStaging(user);
		await screen.findByText("Nothing breaks.");
		await user.click(screen.getByRole("button", { name: "Move service" }));
		expect(
			await screen.findByText(
				"The move breaks references or needs cross-server links removed first. Check again, or move anyway.",
			),
		).toBeVisible();
		expect(closed).toEqual([]);
	});

	it("shows the server's message when the plan itself fails, and offers no move", async () => {
		const user = userEvent.setup();
		stubApi({
			...environments,
			"POST /services/:id/move": problem(409, {
				code: "service_busy",
				detail: "A deployment is running",
			}),
		});
		await open();
		await chooseStaging(user);
		expect(await screen.findByText("A deployment is running")).toBeVisible();
		expect(screen.getByRole("button", { name: "Move service" })).toBeDisabled();
	});

	it("dismisses with Cancel", async () => {
		const user = userEvent.setup();
		stubApi(environments);
		const closed: boolean[] = [];
		await open((value) => closed.push(value));
		await user.click(await screen.findByRole("button", { name: "Cancel" }));
		expect(closed).toEqual([false]);
	});
});
