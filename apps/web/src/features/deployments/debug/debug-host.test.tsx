import { describe, expect, test } from "bun:test";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { deploymentView, logStreamResponse } from "../test-fixtures";
import { DeploymentDebugHost } from "./debug-host";

const routes = {
	"GET /deployments/:id": {
		body: deploymentView({ id: "dep-1" }),
	},
	"GET /services/:id": {
		body: make("ServiceView", { id: "svc-1", name: "billing-api" }),
	},
	"GET /servers": { body: { items: [] } },
	"GET /deployments/:id/events": { body: { items: [] } },
	"GET /deployments/:id/logs": () => logStreamResponse([]),
};

/** The `debug` param as the address holds it right now. */
function DebugParam() {
	const search = useSearch({ strict: false }) as { debug?: string };
	return <output aria-label="debug param">{search.debug ?? "none"}</output>;
}

function Navigate() {
	const navigate = useNavigate();
	return (
		<button
			type="button"
			onClick={() =>
				void navigate({ to: ".", search: { debug: "dep-2" } } as never)
			}
		>
			switch
		</button>
	);
}

describe("DeploymentDebugHost", () => {
	test("renders nothing without a debug param", async () => {
		const api = stubApi(routes);
		await renderWithProviders(<DeploymentDebugHost />, { path: "/" });
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("opens the panel for the deployment named in the address", async () => {
		stubApi(routes);
		await renderWithProviders(<DeploymentDebugHost />, {
			path: "/?debug=dep-1",
		});

		expect(await screen.findByText("deployment dep-1")).toBeInTheDocument();
		expect(await screen.findByText("· billing-api")).toBeInTheDocument();
	});

	test("the close button removes the param and the panel slides away", async () => {
		stubApi(routes);
		await renderWithProviders(
			<>
				<DeploymentDebugHost />
				<DebugParam />
			</>,
			{ path: "/?debug=dep-1" },
		);
		await screen.findByText("· billing-api");
		expect(screen.getByLabelText("debug param")).toHaveTextContent("dep-1");

		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Close the debug panel" }));

		await waitFor(() =>
			expect(screen.getByLabelText("debug param")).toHaveTextContent("none"),
		);
	});

	test("Escape removes the param through the same path", async () => {
		stubApi(routes);
		await renderWithProviders(
			<>
				<DeploymentDebugHost />
				<DebugParam />
			</>,
			{ path: "/?debug=dep-1" },
		);
		await screen.findByText("· billing-api");

		fireEvent.keyDown(document.body, { key: "Escape" });

		await waitFor(() =>
			expect(screen.getByLabelText("debug param")).toHaveTextContent("none"),
		);
	});

	test("pointing the address at another deployment shows that one", async () => {
		stubApi({
			...routes,
			"GET /deployments/dep-2": {
				body: deploymentView({ id: "dep-2" }),
			},
			"GET /deployments/dep-2/events": { body: { items: [] } },
			"GET /deployments/dep-2/logs": () => logStreamResponse([]),
		});
		await renderWithProviders(
			<>
				<DeploymentDebugHost />
				<Navigate />
			</>,
			{ path: "/?debug=dep-1" },
		);
		await screen.findByText("deployment dep-1");

		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "switch" }));

		expect(await screen.findByText("deployment dep-2")).toBeInTheDocument();
		expect(screen.queryByText("deployment dep-1")).not.toBeInTheDocument();
	});
});
