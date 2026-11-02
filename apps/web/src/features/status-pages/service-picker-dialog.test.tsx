import { describe, expect, it, mock } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { TeamService } from "#/features/log-drains/use-team-services";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ServicePickerDialog } from "./service-picker-dialog";

function view(id: string, name: string, environmentId: string) {
	return make("ServiceView", {
		id,
		name,
		slug: name,
		environmentId,
		projectId: "proj-1",
		config: { healthcheck: { type: "http" }, processes: null },
	});
}

function environment(id: string, name: string, projectId: string) {
	return make("Environment", {
		id,
		name,
		slug: name,
		projectId,
		isProduction: false,
	});
}

function stubTeam() {
	return stubApi({
		"GET /projects": {
			body: make("PageProject", {
				items: [
					make("Project", { id: "proj-1", name: "Acme" }),
					make("Project", { id: "proj-2", name: "Beta" }),
				],
			}),
		},
		"GET /projects/proj-1/environments": {
			body: make("PageEnvironment", {
				items: [
					environment("env-1", "production", "proj-1"),
					environment("env-2", "staging", "proj-1"),
				],
			}),
		},
		"GET /projects/proj-2/environments": {
			body: make("PageEnvironment", {
				items: [environment("env-3", "production", "proj-2")],
			}),
		},
		"GET /environments/env-1/services": {
			body: make("PageServiceView", {
				items: [view("web", "web", "env-1"), view("api", "api", "env-1")],
			}),
		},
		"GET /environments/env-2/services": {
			body: make("PageServiceView", {
				items: [view("worker", "worker", "env-2")],
			}),
		},
		"GET /environments/env-3/services": {
			body: make("PageServiceView", {
				items: [view("site", "site", "env-3")],
			}),
		},
	});
}

function Harness({
	taken = new Set<string>(),
	room = 100,
	onAdd = () => {},
	onOpenChange = () => {},
	initiallyOpen = true,
}: {
	taken?: ReadonlySet<string>;
	room?: number;
	onAdd?: (services: TeamService[]) => void;
	onOpenChange?: (open: boolean) => void;
	initiallyOpen?: boolean;
}) {
	const [open, setOpen] = useState(initiallyOpen);
	return (
		<>
			<button type="button" onClick={() => setOpen(true)}>
				reopen
			</button>
			<ServicePickerDialog
				open={open}
				onOpenChange={(next) => {
					onOpenChange(next);
					setOpen(next);
				}}
				taken={taken}
				room={room}
				onAdd={onAdd}
			/>
		</>
	);
}

describe("ServicePickerDialog", () => {
	it("loads nothing while closed", async () => {
		const api = stubTeam();
		await renderWithProviders(<Harness initiallyOpen={false} />);
		expect(api.calls).toEqual([]);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
	});

	it("offers every service of the team under its project and environment", async () => {
		const api = stubTeam();
		await renderWithProviders(<Harness />);
		const dialog = await screen.findByRole("dialog", {
			name: "Add services to the page",
		});
		const prodAcme = await within(dialog).findByRole("group", {
			name: "Acme · production",
		});
		expect(
			within(prodAcme).getByRole("checkbox", { name: "api" }),
		).toBeInTheDocument();
		expect(
			within(prodAcme).getByRole("checkbox", { name: "web" }),
		).toBeInTheDocument();
		expect(
			within(
				within(dialog).getByRole("group", { name: "Acme · staging" }),
			).getByRole("checkbox", { name: "worker" }),
		).toBeInTheDocument();
		expect(
			within(
				within(dialog).getByRole("group", { name: "Beta · production" }),
			).getByRole("checkbox", { name: "site" }),
		).toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	it("does not offer services already on the page", async () => {
		stubTeam();
		await renderWithProviders(<Harness taken={new Set(["web"])} />);
		await screen.findByRole("checkbox", { name: "api" });
		expect(
			screen.queryByRole("checkbox", { name: "web" }),
		).not.toBeInTheDocument();
	});

	it("keeps Add disabled until something is ticked, then counts the picks", async () => {
		stubTeam();
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);
		const add = await screen.findByRole("button", { name: "Add services" });
		expect(add).toBeDisabled();
		expect(screen.getByRole("status")).toHaveTextContent(
			"Tick the services to show.",
		);

		await user.click(await screen.findByRole("checkbox", { name: "web" }));
		expect(screen.getByRole("status")).toHaveTextContent("1 service selected.");
		expect(screen.getByRole("button", { name: "Add 1 service" })).toBeEnabled();

		await user.click(screen.getByRole("checkbox", { name: "site" }));
		expect(screen.getByRole("status")).toHaveTextContent(
			"2 services selected.",
		);
		expect(
			screen.getByRole("button", { name: "Add 2 services" }),
		).toBeEnabled();

		await user.click(screen.getByRole("checkbox", { name: "web" }));
		expect(screen.getByRole("button", { name: "Add 1 service" })).toBeEnabled();
	});

	it("adds the ticked services, with their project and environment, and closes", async () => {
		stubTeam();
		const user = userEvent.setup();
		const onAdd = mock((_services: TeamService[]) => {});
		const onOpenChange = mock((_open: boolean) => {});
		await renderWithProviders(
			<Harness onAdd={onAdd} onOpenChange={onOpenChange} />,
		);
		await user.click(await screen.findByRole("checkbox", { name: "worker" }));
		await user.click(screen.getByRole("checkbox", { name: "api" }));
		await user.click(screen.getByRole("button", { name: "Add 2 services" }));

		expect(onAdd).toHaveBeenCalledTimes(1);
		expect(onAdd.mock.calls[0]?.[0]).toEqual([
			{
				id: "api",
				name: "api",
				projectId: "proj-1",
				projectName: "Acme",
				environmentName: "production",
			},
			{
				id: "worker",
				name: "worker",
				projectId: "proj-1",
				projectName: "Acme",
				environmentName: "staging",
			},
		]);
		expect(onOpenChange).toHaveBeenLastCalledWith(false);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
	});

	it("Cancel closes without adding anything", async () => {
		stubTeam();
		const user = userEvent.setup();
		const onAdd = mock((_services: TeamService[]) => {});
		const onOpenChange = mock((_open: boolean) => {});
		await renderWithProviders(
			<Harness onAdd={onAdd} onOpenChange={onOpenChange} />,
		);
		await user.click(await screen.findByRole("checkbox", { name: "web" }));
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(onAdd).not.toHaveBeenCalled();
		expect(onOpenChange).toHaveBeenLastCalledWith(false);
	});

	it("forgets earlier picks when it opens again", async () => {
		stubTeam();
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);
		await user.click(await screen.findByRole("checkbox", { name: "web" }));
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "reopen" }));
		expect(
			await screen.findByRole("checkbox", { name: "web" }),
		).not.toBeChecked();
		expect(screen.getByRole("button", { name: "Add services" })).toBeDisabled();
	});

	it("stops picking at the room the page has left", async () => {
		stubTeam();
		const user = userEvent.setup();
		const onAdd = mock((_services: TeamService[]) => {});
		await renderWithProviders(<Harness room={2} onAdd={onAdd} />);
		await user.click(await screen.findByRole("checkbox", { name: "api" }));
		await user.click(screen.getByRole("checkbox", { name: "web" }));
		expect(screen.getByRole("status")).toHaveTextContent(
			"The page has room for 2 more components; that is all of them.",
		);

		await user.click(screen.getByRole("checkbox", { name: "site" }));
		expect(screen.getByRole("checkbox", { name: "site" })).not.toBeChecked();
		await user.click(screen.getByRole("button", { name: "Add 2 services" }));
		expect(onAdd.mock.calls[0]?.[0].map((s) => s.id)).toEqual(["api", "web"]);
	});

	it("frees a slot when a pick is unticked", async () => {
		stubTeam();
		const user = userEvent.setup();
		await renderWithProviders(<Harness room={1} />);
		await user.click(await screen.findByRole("checkbox", { name: "api" }));
		expect(screen.getByRole("status")).toHaveTextContent(
			"The page has room for 1 more component; that is all of them.",
		);
		await user.click(screen.getByRole("checkbox", { name: "api" }));
		await user.click(screen.getByRole("checkbox", { name: "web" }));
		expect(screen.getByRole("checkbox", { name: "web" })).toBeChecked();
		expect(screen.getByRole("checkbox", { name: "api" })).not.toBeChecked();
	});

	it("says the page is full when there is no room at all", async () => {
		stubTeam();
		await renderWithProviders(<Harness room={0} />);
		await screen.findByRole("checkbox", { name: "web" });
		expect(screen.getByRole("status")).toHaveTextContent(
			"The page already shows the most it can, 100 components.",
		);
	});
});
