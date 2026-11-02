import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { CreateTeamDialog } from "./create-team-dialog";

function Harness({ initiallyOpen = true }: { initiallyOpen?: boolean }) {
	const [open, setOpen] = useState(initiallyOpen);
	return (
		<>
			<button type="button" onClick={() => setOpen(true)}>
				open dialog
			</button>
			<span data-testid="state">{open ? "open" : "closed"}</span>
			<CreateTeamDialog open={open} onOpenChange={setOpen} />
		</>
	);
}

const team = make("Team", {
	id: "t-9",
	name: "Platform",
	slug: "platform",
	role: "owner",
	current: false,
	memberCount: 1,
	createdAt: "2026-01-01T00:00:00Z",
});

describe("CreateTeamDialog", () => {
	test("keeps Create team disabled until the name has non-blank text", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);

		const submit = await screen.findByRole("button", { name: "Create team" });
		expect(submit).toBeDisabled();

		await user.type(screen.getByLabelText("Team name"), "   ");
		expect(submit).toBeDisabled();

		await user.type(screen.getByLabelText("Team name"), "Platform");
		expect(submit).toBeEnabled();
		expect(api.calls).toEqual([]);
	});

	test("creates the team with the trimmed name, switches into it and closes", async () => {
		const api = stubApi({
			"POST /teams": { status: 201, body: team },
			"POST /teams/:teamId/switch": { body: { ...team, current: true } },
		});
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);

		await user.type(await screen.findByLabelText("Team name"), "  Platform  ");
		await user.click(screen.getByRole("button", { name: "Create team" }));

		expect(await screen.findByText("Created Platform")).toBeInTheDocument();
		expect(screen.getByText("You are its owner.")).toBeInTheDocument();
		expect(screen.getByTestId("state")).toHaveTextContent("closed");
		expect(api.calls.map((c) => `${c.method} ${c.path}`)).toEqual([
			"POST /teams",
			"POST /teams/t-9/switch",
		]);
		expect(api.calls[0]?.body).toEqual({ name: "Platform" });
		expect(api.unmatched).toEqual([]);
	});

	test("explains that the administrator turned team creation off", async () => {
		const api = stubApi({
			"POST /teams": {
				status: 403,
				body: {
					status: 403,
					code: "team_creation_disabled",
					title: "Forbidden",
					detail: "team creation is disabled",
				},
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);

		await user.type(await screen.findByLabelText("Team name"), "Platform");
		await user.click(screen.getByRole("button", { name: "Create team" }));

		expect(
			await screen.findByText(
				"The instance administrator has turned team creation off.",
			),
		).toBeInTheDocument();
		// A refused create never switches teams, and the dialog stays open.
		expect(api.calls.map((c) => c.path)).toEqual(["/teams"]);
		expect(screen.getByTestId("state")).toHaveTextContent("open");
	});

	test("shows the server's reason for any other failure", async () => {
		stubApi({
			"POST /teams": {
				status: 422,
				body: {
					status: 422,
					code: "invalid_name",
					title: "Unprocessable",
					detail: "name is already taken",
				},
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);

		await user.type(await screen.findByLabelText("Team name"), "Platform");
		await user.click(screen.getByRole("button", { name: "Create team" }));

		expect(
			await screen.findByText("name is already taken"),
		).toBeInTheDocument();
		expect(
			screen.queryByText(/turned team creation off/),
		).not.toBeInTheDocument();
	});

	test("Cancel closes the dialog and reopening starts with an empty name", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);

		await user.type(await screen.findByLabelText("Team name"), "Half typed");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.getByTestId("state")).toHaveTextContent("closed"),
		);

		await user.click(screen.getByRole("button", { name: "open dialog" }));
		expect(await screen.findByLabelText("Team name")).toHaveValue("");
		expect(api.calls).toEqual([]);
	});
});
