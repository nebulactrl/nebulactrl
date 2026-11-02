import { expect, test } from "bun:test";
import { useQuery } from "@tanstack/react-query";
import { act, screen, waitFor } from "@testing-library/react";
import { listEnvironmentDeploymentsOptions } from "#/api/gen/@tanstack/react-query.gen";
import { linksQuery, servicesQuery } from "#/api/hooks";
import {
	type OpenReviewOptions,
	onOpenStagedReview,
} from "#/features/deploy-button/open-review";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import {
	type CanvasMenuActions,
	useCanvasMenuActions,
} from "./use-canvas-menu-actions";

type ActionsRef = { current: CanvasMenuActions | undefined };

function Probe({
	isProduction,
	actionsRef,
}: {
	isProduction: boolean;
	actionsRef: ActionsRef;
}) {
	actionsRef.current = useCanvasMenuActions(isProduction);
	return null;
}

/** Also keeps the services and deployments queries mounted so their refresh is observable. */
function ServicesAndDeploymentsProbe({
	actionsRef,
}: {
	actionsRef: ActionsRef;
}) {
	actionsRef.current = useCanvasMenuActions(false);
	useQuery(servicesQuery("env-1"));
	useQuery(
		listEnvironmentDeploymentsOptions({ path: { environmentId: "env-1" } }),
	);
	return null;
}

/** Also keeps the services query mounted so its refresh is observable. */
function ServicesProbe({ actionsRef }: { actionsRef: ActionsRef }) {
	actionsRef.current = useCanvasMenuActions(false);
	useQuery(servicesQuery("env-1"));
	return null;
}

/** Also keeps the links query mounted so its refresh is observable. */
function LinksProbe({ actionsRef }: { actionsRef: ActionsRef }) {
	actionsRef.current = useCanvasMenuActions(false);
	useQuery(linksQuery("env-1"));
	return null;
}

async function mountActions(isProduction: boolean) {
	const ref: { current: CanvasMenuActions | undefined } = {
		current: undefined,
	};
	await renderWithProviders(
		<Probe isProduction={isProduction} actionsRef={ref} />,
	);
	const actions = () => {
		if (!ref.current) throw new Error("not mounted");
		return ref.current;
	};
	return actions;
}

const ok = {
	status: 202,
	body: make("DeploymentView", { id: "dep-1", status: "queued" }),
};
const restarted = { status: 202, body: make("ActionResult") };
const serverError = (detail: string) =>
	problem(500, { title: "Internal Server Error", detail });

test("deploying a selection posts one deployment per service, each with its own idempotency key", async () => {
	const keys: Array<string | null> = [];
	const api = stubApi({
		"POST /services/:id/deployments": (request) => {
			keys.push(request.headers.get("Idempotency-Key"));
			return Response.json({ id: "dep-1" }, { status: 202 });
		},
	});
	const actions = await mountActions(false);
	await act(() => actions().deploySelected(["a", "b"]));
	expect(api.calls.map((c) => `${c.method} ${c.path}`).sort()).toEqual([
		"POST /services/a/deployments",
		"POST /services/b/deployments",
	]);
	expect(keys[0]).toBeTruthy();
	expect(keys[0]).not.toBe(keys[1]);
	expect(await screen.findByText("Deploying 2 services")).toBeInTheDocument();
	expect(screen.getByText("Watch each card for progress.")).toBeInTheDocument();
});

test("deploying one service says so in the singular", async () => {
	stubApi({ "POST /services/:id/deployments": ok });
	const actions = await mountActions(false);
	await act(() => actions().deploySelected(["a"]));
	expect(await screen.findByText("Deploying 1 service")).toBeInTheDocument();
});

test("an empty selection sends nothing and says nothing", async () => {
	const api = stubApi({});
	const actions = await mountActions(false);
	await act(() => actions().deploySelected([]));
	await act(() => actions().restartSelected([]));
	expect(api.calls).toEqual([]);
	expect(api.unmatched).toEqual([]);
	expect(screen.queryByRole("status")).toBeNull();
});

test("in production the deploy opens the review sheet and sends nothing", async () => {
	const api = stubApi({});
	const opened: OpenReviewOptions[] = [];
	const off = onOpenStagedReview((options) => opened.push(options));
	const actions = await mountActions(true);
	await act(() => actions().deploySelected(["a", "b"]));
	off();
	expect(opened).toEqual([{}]);
	expect(api.calls).toEqual([]);
});

test("a partly failed deploy reports both the services started and the failure", async () => {
	stubApi({
		"POST /services/a/deployments": ok,
		"POST /services/b/deployments": serverError("agent offline"),
		"POST /services/c/deployments": serverError("agent offline"),
	});
	const actions = await mountActions(false);
	await act(() => actions().deploySelected(["a", "b", "c"]));
	expect(await screen.findByText("Deploying 1 service")).toBeInTheDocument();
	expect(screen.getByText("Could not deploy 2 services")).toBeInTheDocument();
	expect(screen.getByText("agent offline")).toBeInTheDocument();
});

test("when the only deploy fails there is no success toast", async () => {
	stubApi({ "POST /services/:id/deployments": serverError("no capacity") });
	const actions = await mountActions(false);
	await act(() => actions().deploySelected(["a"]));
	expect(
		await screen.findByText("Could not deploy 1 service"),
	).toBeInTheDocument();
	expect(screen.getByText("no capacity")).toBeInTheDocument();
	expect(screen.queryByText(/^Deploying/)).toBeNull();
});

test("a deploy refreshes the services and the environment's deployments on screen", async () => {
	const api = stubApi({
		"POST /services/:id/deployments": ok,
		"GET /environments/:id/services": { body: { items: [] } },
		"GET /environments/:id/deployments": { body: { items: [] } },
	});
	const ref: { current: CanvasMenuActions | undefined } = {
		current: undefined,
	};
	await renderWithProviders(<ServicesAndDeploymentsProbe actionsRef={ref} />);
	await waitFor(() => expect(api.calls).toHaveLength(2));
	await act(() => ref.current?.deploySelected(["a"]) ?? Promise.resolve());
	const gets = api.calls.filter((c) => c.method === "GET").map((c) => c.path);
	expect(gets.sort()).toEqual([
		"/environments/env-1/deployments",
		"/environments/env-1/deployments",
		"/environments/env-1/services",
		"/environments/env-1/services",
	]);
});

test("a restart refreshes the services too", async () => {
	const api = stubApi({
		"POST /services/:id/restart": restarted,
		"GET /environments/:id/services": { body: { items: [] } },
	});
	const ref: { current: CanvasMenuActions | undefined } = {
		current: undefined,
	};
	await renderWithProviders(<ServicesProbe actionsRef={ref} />);
	await waitFor(() => expect(api.calls).toHaveLength(1));
	await act(() => ref.current?.restartSelected(["a"]) ?? Promise.resolve());
	expect(
		api.calls.filter((c) => c.path === "/environments/env-1/services"),
	).toHaveLength(2);
});

test("removing a link refreshes the environment's links", async () => {
	const api = stubApi({
		"DELETE /links/:id": { status: 204 },
		"GET /environments/:id/links": { body: { items: [] } },
	});
	const ref: { current: CanvasMenuActions | undefined } = {
		current: undefined,
	};
	await renderWithProviders(<LinksProbe actionsRef={ref} />);
	await waitFor(() => expect(api.calls).toHaveLength(1));
	await act(() => ref.current?.removeLink({ id: "l1" }) ?? Promise.resolve());
	expect(
		api.calls.filter((c) => c.path === "/environments/env-1/links"),
	).toHaveLength(2);
});

test("restarting a selection posts one restart per service", async () => {
	const api = stubApi({ "POST /services/:id/restart": restarted });
	const actions = await mountActions(false);
	await act(() => actions().restartSelected(["a", "b", "c"]));
	expect(api.calls.map((c) => `${c.method} ${c.path}`).sort()).toEqual([
		"POST /services/a/restart",
		"POST /services/b/restart",
		"POST /services/c/restart",
	]);
	expect(await screen.findByText("Restarting 3 services")).toBeInTheDocument();
	expect(
		screen.getByText("The commands were sent to their servers."),
	).toBeInTheDocument();
});

test("restarting one service says so in the singular, even in production", async () => {
	stubApi({ "POST /services/:id/restart": restarted });
	const actions = await mountActions(true);
	await act(() => actions().restartSelected(["a"]));
	expect(await screen.findByText("Restarting 1 service")).toBeInTheDocument();
});

test("a failed restart is reported with the server's reason", async () => {
	stubApi({
		"POST /services/a/restart": restarted,
		"POST /services/b/restart": serverError("server unreachable"),
	});
	const actions = await mountActions(false);
	await act(() => actions().restartSelected(["a", "b"]));
	expect(await screen.findByText("Restarting 1 service")).toBeInTheDocument();
	expect(screen.getByText("Could not restart 1 service")).toBeInTheDocument();
	expect(screen.getByText("server unreachable")).toBeInTheDocument();
});

test("every restart failing reports the count and no success", async () => {
	stubApi({ "POST /services/:id/restart": serverError("down") });
	const actions = await mountActions(false);
	await act(() => actions().restartSelected(["a", "b"]));
	expect(
		await screen.findByText("Could not restart 2 services"),
	).toBeInTheDocument();
	expect(screen.queryByText(/^Restarting/)).toBeNull();
});

test("removing a link deletes it and reassures that nothing else changed", async () => {
	const api = stubApi({ "DELETE /links/:id": { status: 204 } });
	const actions = await mountActions(false);
	await act(() => actions().removeLink({ id: "link-7" }));
	expect(api.calls.map((c) => `${c.method} ${c.path}`)).toEqual([
		"DELETE /links/link-7",
	]);
	expect(await screen.findByText("Link removed")).toBeInTheDocument();
	expect(
		screen.getByText("The services and their variables are unchanged."),
	).toBeInTheDocument();
});

test("a link that cannot be removed shows why, with no success message", async () => {
	stubApi({
		"DELETE /links/:id": problem(404, {
			title: "Not Found",
			detail: "link already gone",
		}),
	});
	const actions = await mountActions(false);
	await act(() => actions().removeLink({ id: "link-7" }));
	expect(
		await screen.findByText("Could not remove the link"),
	).toBeInTheDocument();
	expect(screen.getByText("link already gone")).toBeInTheDocument();
	expect(screen.queryByText("Link removed")).toBeNull();
});
