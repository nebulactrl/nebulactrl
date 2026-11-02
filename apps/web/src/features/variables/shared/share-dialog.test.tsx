import { describe, expect, it } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { Service } from "#/api/models";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ShareDialog } from "./share-dialog";
import type { SharedOwner } from "./use-shared-mutations";

const SERVICES = [
	{ id: "s-web", name: "Web", slug: "web" },
	{ id: "s-api", name: "API", slug: "api" },
] as Service[];

function Harness({
	owner = { layer: "project", id: "p1" },
	services = SERVICES,
	onClosed,
}: {
	owner?: SharedOwner;
	services?: readonly Service[];
	onClosed?: () => void;
}) {
	const [key, setKey] = useState<string | null>("SESSION");
	return (
		<ShareDialog
			owner={owner}
			variableKey={key}
			services={services}
			onClose={() => {
				setKey(null);
				onClosed?.();
			}}
		/>
	);
}

const stageButton = () => screen.getByRole("button", { name: /^Stage on/ });

describe("ShareDialog", () => {
	it("lists the services as checkboxes with the stage button locked until one is ticked", async () => {
		const user = userEvent.setup();
		stubApi({});
		await renderWithProviders(<Harness />);

		expect(
			await screen.findByRole("heading", { name: "Share SESSION" }),
		).toBeInTheDocument();
		const list = screen.getByRole("list", { name: "Services" });
		expect(within(list).getAllByRole("checkbox")).toHaveLength(2);
		expect(stageButton()).toBeDisabled();
		expect(stageButton()).toHaveTextContent("Stage on services");

		await user.click(screen.getByRole("checkbox", { name: /Web/ }));
		expect(stageButton()).toBeEnabled();
		expect(stageButton()).toHaveTextContent("Stage on 1 service");
		await user.click(screen.getByRole("checkbox", { name: /API/ }));
		expect(stageButton()).toHaveTextContent("Stage on 2 services");
		await user.click(screen.getByRole("checkbox", { name: /Web/ }));
		await user.click(screen.getByRole("checkbox", { name: /API/ }));
		expect(stageButton()).toBeDisabled();
	});

	it("posts the chosen service ids to the project layer, toasts and closes", async () => {
		const user = userEvent.setup();
		let closed = 0;
		const api = stubApi({
			"POST /projects/p1/shared-variables/:key/share": {
				body: make("SharedVariableList"),
			},
			"GET /environments/:id/services": { body: make("PageServiceView") },
		});
		await renderWithProviders(<Harness onClosed={() => closed++} />);
		await user.click(await screen.findByRole("checkbox", { name: /API/ }));
		await user.click(stageButton());

		expect(await screen.findByText("Shared SESSION")).toBeInTheDocument();
		expect(
			screen.getByText("Staged on the chosen services until the next deploy."),
		).toBeInTheDocument();
		const post = api.calls.find((call) => call.method === "POST");
		expect(post?.path).toBe("/projects/p1/shared-variables/SESSION/share");
		expect(post?.body).toEqual({ serviceIds: ["s-api"] });
		expect(closed).toBe(1);
		expect(screen.queryByRole("heading", { name: "Share SESSION" })).toBeNull();
	});

	it("uses the team endpoint and the team. reference wording for a team variable", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"POST /team/shared-variables/:key/share": {
				body: make("SharedVariableList"),
			},
		});
		await renderWithProviders(<Harness owner={{ layer: "team" }} />);
		expect(
			await screen.findByText(/Stages SESSION = team\.\s*SESSION/),
		).toBeInTheDocument();
		await user.click(screen.getByRole("checkbox", { name: /Web/ }));
		await user.click(stageButton());
		await waitFor(() =>
			expect(api.calls.map((c) => c.path)).toContain(
				"/team/shared-variables/SESSION/share",
			),
		);
		expect(api.calls[0]?.body).toEqual({ serviceIds: ["s-web"] });
	});

	it("describes an environment variable as a shared. reference", async () => {
		stubApi({});
		await renderWithProviders(
			<Harness owner={{ layer: "environment", id: "e1" }} />,
		);
		expect(
			await screen.findByText(/Stages SESSION = shared\.\s*SESSION/),
		).toBeInTheDocument();
	});

	it("shows each warning from the server as its own toast", async () => {
		const user = userEvent.setup();
		stubApi({
			"POST /environments/e1/shared-variables/:key/share": {
				body: make("SharedVariableList", {
					warnings: [
						{ code: "exists", message: "web already defines SESSION" },
						{ code: "secret", message: "SESSION is a secret" },
					],
				}),
			},
			"GET /environments/:id/services": { body: make("PageServiceView") },
		});
		await renderWithProviders(
			<Harness owner={{ layer: "environment", id: "e1" }} />,
		);
		await user.click(await screen.findByRole("checkbox", { name: /Web/ }));
		await user.click(stageButton());
		expect(
			await screen.findByText("web already defines SESSION"),
		).toBeInTheDocument();
		expect(screen.getByText("SESSION is a secret")).toBeInTheDocument();
	});

	it("keeps the dialog open and shows the error when sharing fails", async () => {
		const user = userEvent.setup();
		let closed = 0;
		stubApi({
			"POST /projects/p1/shared-variables/:key/share": problem(403, {
				code: "forbidden",
				title: "Forbidden",
				detail: "not allowed to share",
			}),
		});
		await renderWithProviders(<Harness onClosed={() => closed++} />);
		await user.click(await screen.findByRole("checkbox", { name: /Web/ }));
		await user.click(stageButton());

		expect(await screen.findByRole("alert")).toHaveTextContent(
			/not allowed to share/,
		);
		expect(closed).toBe(0);
		expect(
			screen.getByRole("heading", { name: "Share SESSION" }),
		).toBeVisible();
		expect(stageButton()).toBeEnabled();
	});

	it("says the environment has no services and offers nothing to stage", async () => {
		stubApi({});
		await renderWithProviders(<Harness services={[]} />);
		expect(
			await screen.findByText("This environment has no services yet."),
		).toBeInTheDocument();
		expect(screen.queryByRole("list", { name: "Services" })).toBeNull();
		expect(stageButton()).toBeDisabled();
	});

	it("Cancel closes without calling the API", async () => {
		const user = userEvent.setup();
		let closed = 0;
		const api = stubApi({});
		await renderWithProviders(<Harness onClosed={() => closed++} />);
		await user.click(await screen.findByRole("checkbox", { name: /Web/ }));
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(closed).toBe(1);
		expect(api.calls).toEqual([]);
		expect(screen.queryByRole("heading", { name: "Share SESSION" })).toBeNull();
	});
});
