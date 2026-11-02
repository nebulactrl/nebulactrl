import { afterEach, describe, expect, test } from "bun:test";
import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Service } from "#/api/models";
import { onOpenStagedReview } from "#/features/deploy-button/open-review";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { StackActions } from "./stack-actions";
import { useTemplatesState } from "./store";
import {
	contextRoutes,
	ENV_PATH,
	ENV_ROUTE,
	environment,
	instance,
	resetTemplatesStore,
} from "./test-fixtures";

afterEach(() => {
	act(() => {
		resetTemplatesStore();
	});
});

const member = (id: string, staged: boolean) =>
	({ id, hasStagedChanges: staged }) as Service;

function Probe() {
	const { deleting, updating } = useTemplatesState();
	return (
		<output aria-label="requests">
			{`delete=${deleting?.id ?? "-"} update=${updating?.id ?? "-"}`}
		</output>
	);
}

function setup(opts: {
	stacks: ReturnType<typeof instance>[];
	members?: Service[];
	production?: boolean;
	groupId?: string;
}) {
	const api = stubApi({
		...contextRoutes(environment({ isProduction: opts.production ?? false })),
		"GET /environments/:environmentId/template-instances": {
			body: opts.stacks,
		},
		"POST /environments/:environmentId/deploy-staged": {
			status: 202,
			body: make("StagedResult", {
				items: [make("DeploymentView", { id: "d1" })],
			}),
		},
	});
	return {
		api,
		render: () =>
			renderWithProviders(
				<>
					<StackActions
						groupId={opts.groupId ?? "g1"}
						groupName="Umami stack"
						members={opts.members ?? []}
					/>
					<Probe />
				</>,
				{ path: ENV_PATH, routePath: ENV_ROUTE },
			),
	};
}

describe("StackActions", () => {
	test("renders nothing for a group that is not a template stack", async () => {
		const { api, render } = setup({
			stacks: [instance({ id: "i1", groupId: "other" })],
		});
		const { container } = await render();
		await waitFor(() =>
			expect(
				api.calls.some((c) => c.path.endsWith("/template-instances")),
			).toBe(true),
		);
		expect(screen.queryByRole("button")).not.toBeInTheDocument();
		expect(container.querySelector("button")).toBeNull();
	});

	test("a stack with nothing staged has a disabled deploy button that says so", async () => {
		const { render } = setup({
			stacks: [instance({ id: "i1" })],
			members: [member("a", false)],
		});
		await render();
		const deploy = await screen.findByRole("button", {
			name: "Deploy stack: nothing is staged in it: Umami stack",
		});
		expect(deploy).toBeDisabled();
	});

	test("there is no update button unless a newer revision exists", async () => {
		const { render } = setup({
			stacks: [instance({ id: "i1", updateAvailable: false })],
		});
		await render();
		await screen.findByRole("button", { name: /Delete stack/ });
		expect(
			screen.queryByRole("button", { name: /Update available/ }),
		).not.toBeInTheDocument();
	});

	test("the update button names both versions and asks for that stack's update", async () => {
		const user = userEvent.setup();
		const { render } = setup({
			stacks: [
				instance({
					id: "i1",
					updateAvailable: true,
					templateVersion: "1.0.0",
					currentVersion: "1.1.0",
				}),
			],
		});
		await render();
		await user.click(
			await screen.findByRole("button", {
				name: "Update available: 1.0.0 → 1.1.0, Umami stack",
			}),
		);
		expect(screen.getByLabelText("requests")).toHaveTextContent("update=i1");
	});

	test("without versions the update button falls back to revision numbers", async () => {
		const { render } = setup({
			stacks: [
				instance({ id: "i1", updateAvailable: true, currentRevision: 3 }),
			],
		});
		await render();
		expect(
			await screen.findByRole("button", {
				name: "Update available: revision 3 → revision 3, Umami stack",
			}),
		).toBeInTheDocument();
	});

	test("the delete button asks for that stack's deletion", async () => {
		const user = userEvent.setup();
		const { render } = setup({ stacks: [instance({ id: "i1" })] });
		await render();
		await user.click(
			await screen.findByRole("button", { name: "Delete stack Umami stack" }),
		);
		expect(screen.getByLabelText("requests")).toHaveTextContent("delete=i1");
	});

	test("deploying sends only the staged members of the stack", async () => {
		const user = userEvent.setup();
		const { api, render } = setup({
			stacks: [instance({ id: "i1" })],
			members: [member("a", true), member("b", false), member("c", true)],
		});
		await render();
		const deploy = await screen.findByRole("button", {
			name: "Deploy stack (2 staged): Umami stack",
		});
		await user.click(deploy);
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
				serviceIds: ["a", "c"],
			}),
		);
		expect(api.calls.find((c) => c.method === "POST")?.path).toBe(
			"/environments/env1/deploy-staged",
		);
		expect(await screen.findByText("Deploying 1 service")).toBeInTheDocument();
	});

	test("in production deploy opens the review sheet and sends nothing", async () => {
		const user = userEvent.setup();
		let opened = 0;
		const off = onOpenStagedReview(() => {
			opened += 1;
		});
		try {
			const { api, render } = setup({
				stacks: [instance({ id: "i1" })],
				members: [member("a", true)],
				production: true,
			});
			await render();
			await user.click(
				await screen.findByRole("button", {
					name: "Deploy stack (1 staged): Umami stack",
				}),
			);
			expect(opened).toBe(1);
			expect(api.calls.some((c) => c.method === "POST")).toBe(false);
		} finally {
			off();
		}
	});

	test("a failed deploy tells the user and re-enables the button", async () => {
		const user = userEvent.setup();
		stubApi({
			...contextRoutes(),
			"GET /environments/:environmentId/template-instances": {
				body: [instance({ id: "i1" })],
			},
			"POST /environments/:environmentId/deploy-staged": problem(500, {
				status: 500,
				title: "x",
				detail: "agent offline",
				code: "internal",
			}),
		});
		await renderWithProviders(
			<StackActions
				groupId="g1"
				groupName="Umami stack"
				members={[member("a", true)]}
			/>,
			{ path: ENV_PATH, routePath: ENV_ROUTE },
		);
		const deploy = await screen.findByRole("button", {
			name: "Deploy stack (1 staged): Umami stack",
		});
		await user.click(deploy);
		expect(
			await screen.findByText("Could not deploy the changes"),
		).toBeInTheDocument();
		await waitFor(() => expect(deploy).toBeEnabled());
	});
});
