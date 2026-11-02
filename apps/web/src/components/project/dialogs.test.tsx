import { describe, expect, it, jest } from "bun:test";
import { useLocation } from "@tanstack/react-router";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders, testQueryClient } from "#/test/render";
import { NewEnvironmentDialog } from "./new-environment-dialog";
import { NewProjectDialog } from "./new-project-dialog";

function Where() {
	const pathname = useLocation({ select: (l) => l.pathname });
	return <p data-testid="where">{pathname}</p>;
}

function ProjectHost({
	onOpenChange,
}: {
	onOpenChange?: (open: boolean) => void;
}) {
	const [open, setOpen] = useState(true);
	return (
		<>
			<Where />
			<button type="button" onClick={() => setOpen(true)}>
				Reopen
			</button>
			<NewProjectDialog
				open={open}
				onOpenChange={(next) => {
					setOpen(next);
					onOpenChange?.(next);
				}}
			/>
		</>
	);
}

function EnvHost() {
	const [open, setOpen] = useState(true);
	return (
		<>
			<Where />
			<button type="button" onClick={() => setOpen(true)}>
				Reopen
			</button>
			<NewEnvironmentDialog projectId="p1" open={open} onOpenChange={setOpen} />
		</>
	);
}

const createButton = () =>
	screen.getByRole("button", { name: "Create project" });

describe("NewProjectDialog", () => {
	it("is titled New project with Create locked until a name is typed", async () => {
		await renderWithProviders(<ProjectHost />);
		expect(
			await screen.findByRole("dialog", { name: "New project" }),
		).toBeInTheDocument();
		expect(createButton()).toBeDisabled();
		const user = userEvent.setup();
		await user.type(screen.getByLabelText("Name"), "   ");
		expect(createButton()).toBeDisabled();
		await user.type(screen.getByLabelText("Name"), "shop");
		expect(createButton()).toBeEnabled();
	});

	it("creates the project with a trimmed name and description, then opens its production canvas", async () => {
		const api = stubApi({
			"POST /projects": {
				status: 201,
				body: make("Project", {
					id: "p9",
					name: "shop",
					slug: "shop",
					description: "Storefront",
				}),
			},
			"GET /projects": { body: { items: [] } },
		});
		const onOpenChange = jest.fn();
		const user = userEvent.setup();
		await renderWithProviders(<ProjectHost onOpenChange={onOpenChange} />);
		await user.type(await screen.findByLabelText("Name"), "  shop  ");
		await user.type(
			screen.getByLabelText("Description (optional)"),
			" Storefront ",
		);
		await user.click(createButton());
		await waitFor(() =>
			expect(screen.getByTestId("where")).toHaveTextContent("/p/p9/production"),
		);
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			name: "shop",
			description: "Storefront",
		});
		expect(onOpenChange).toHaveBeenCalledWith(false);
		expect(api.unmatched).toEqual([]);
	});

	it("leaves the description out when it is blank", async () => {
		const api = stubApi({
			"POST /projects": {
				status: 201,
				body: make("Project", { id: "p9", name: "shop", slug: "shop" }),
			},
			"GET /projects": { body: { items: [] } },
		});
		const user = userEvent.setup();
		await renderWithProviders(<ProjectHost />);
		await user.type(await screen.findByLabelText("Name"), "shop");
		await user.type(screen.getByLabelText("Description (optional)"), "   ");
		await user.click(createButton());
		await waitFor(() =>
			expect(api.calls.some((c) => c.method === "POST")).toBe(true),
		);
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			name: "shop",
		});
	});

	it("sends one Idempotency-Key per opening and a fresh one when reopened", async () => {
		const keys: string[] = [];
		stubApi({
			"POST /projects": (request) => {
				keys.push(request.headers.get("Idempotency-Key") ?? "");
				return Response.json(
					{ code: "conflict", detail: "name taken", status: 409 },
					{ status: 409 },
				);
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<ProjectHost />);
		await user.type(await screen.findByLabelText("Name"), "shop");
		await user.click(createButton());
		await screen.findByText("name taken");
		await user.click(createButton());
		await waitFor(() => expect(keys).toHaveLength(2));
		// A retry within one opening repeats the key: a double submit creates one project.
		expect(keys[0]).toMatch(/^[0-9a-f-]{36}$/);
		expect(keys[1]).toBe(keys[0] as string);

		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		await user.click(screen.getByRole("button", { name: "Reopen" }));
		await user.type(await screen.findByLabelText("Name"), "shop");
		await user.click(createButton());
		await waitFor(() => expect(keys).toHaveLength(3));
		expect(keys[2]).not.toBe(keys[0]);
	});

	it("shows the server's reason and stays open on failure", async () => {
		stubApi({
			"POST /projects": problem(409, {
				code: "conflict",
				detail: "A project named shop exists.",
			}),
		});
		const user = userEvent.setup();
		await renderWithProviders(<ProjectHost />);
		await user.type(await screen.findByLabelText("Name"), "shop");
		await user.click(createButton());
		expect(
			await screen.findByText("A project named shop exists."),
		).toBeInTheDocument();
		expect(
			screen.getByRole("dialog", { name: "New project" }),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Name")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(screen.getByTestId("where")).toHaveTextContent("/");
	});

	it("does not post an empty name on Enter", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		await renderWithProviders(<ProjectHost />);
		await screen.findByLabelText("Name");
		await user.type(
			screen.getByLabelText("Description (optional)"),
			"x{Enter}",
		);
		await user.type(screen.getByLabelText("Name"), "{Enter}");
		expect(api.calls).toHaveLength(0);
	});

	it("submits with Enter in the name field", async () => {
		const api = stubApi({
			"POST /projects": {
				status: 201,
				body: make("Project", { id: "p2", name: "a", slug: "a" }),
			},
			"GET /projects": { body: { items: [] } },
		});
		const user = userEvent.setup();
		await renderWithProviders(<ProjectHost />);
		await user.type(await screen.findByLabelText("Name"), "a{Enter}");
		await waitFor(() =>
			expect(screen.getByTestId("where")).toHaveTextContent("/p/p2/production"),
		);
		expect(api.calls.filter((c) => c.method === "POST")).toHaveLength(1);
	});

	it("clears the fields when reopened", async () => {
		stubApi({});
		const user = userEvent.setup();
		await renderWithProviders(<ProjectHost />);
		await user.type(await screen.findByLabelText("Name"), "draft");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		await user.click(screen.getByRole("button", { name: "Reopen" }));
		expect(await screen.findByLabelText("Name")).toHaveValue("");
	});

	it("refreshes the project list after creating", async () => {
		const queryClient = testQueryClient();
		const invalidate = jest.spyOn(queryClient, "invalidateQueries");
		stubApi({
			"POST /projects": {
				status: 201,
				body: make("Project", { id: "p2", name: "a", slug: "a" }),
			},
			"GET /projects": { body: { items: [] } },
		});
		const user = userEvent.setup();
		await renderWithProviders(<ProjectHost />, { queryClient });
		await user.type(await screen.findByLabelText("Name"), "a");
		await user.click(createButton());
		await waitFor(() => expect(invalidate).toHaveBeenCalledTimes(1));
	});
});

const envCreate = () =>
	screen.getByRole("button", { name: "Create environment" });

describe("NewEnvironmentDialog", () => {
	it("is titled New environment with Create locked until a name is typed", async () => {
		await renderWithProviders(<EnvHost />);
		expect(
			await screen.findByRole("dialog", { name: "New environment" }),
		).toBeInTheDocument();
		expect(envCreate()).toBeDisabled();
		await userEvent.setup().type(screen.getByLabelText("Name"), "staging");
		expect(envCreate()).toBeEnabled();
	});

	it("creates a non-production environment and opens its canvas by slug", async () => {
		const api = stubApi({
			"POST /projects/:projectId/environments": {
				status: 201,
				body: make("Environment", {
					id: "e5",
					name: "staging",
					slug: "staging-2",
					projectId: "p1",
				}),
			},
			"GET /projects/:projectId/environments": { body: { items: [] } },
		});
		const user = userEvent.setup();
		await renderWithProviders(<EnvHost />);
		await user.type(await screen.findByLabelText("Name"), " staging ");
		await user.click(envCreate());
		await waitFor(() =>
			expect(screen.getByTestId("where")).toHaveTextContent("/p/p1/staging-2"),
		);
		const post = api.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/projects/p1/environments");
		expect(post?.body).toEqual({ name: "staging", isProduction: false });
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});

	it("sends isProduction when the Production box is ticked", async () => {
		const api = stubApi({
			"POST /projects/:projectId/environments": {
				status: 201,
				body: make("Environment", {
					id: "e5",
					name: "prod2",
					slug: "prod2",
					projectId: "p1",
				}),
			},
			"GET /projects/:projectId/environments": { body: { items: [] } },
		});
		const user = userEvent.setup();
		await renderWithProviders(<EnvHost />);
		await user.type(await screen.findByLabelText("Name"), "prod2");
		await user.click(screen.getByRole("checkbox", { name: /Production/ }));
		await user.click(envCreate());
		await waitFor(() =>
			expect(api.calls.some((c) => c.method === "POST")).toBe(true),
		);
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			name: "prod2",
			isProduction: true,
		});
	});

	it("shows the server's reason and stays open on failure", async () => {
		stubApi({
			"POST /projects/:projectId/environments": problem(409, {
				code: "conflict",
				detail: "That name is taken.",
			}),
		});
		const user = userEvent.setup();
		await renderWithProviders(<EnvHost />);
		await user.type(await screen.findByLabelText("Name"), "dup");
		await user.click(envCreate());
		expect(await screen.findByText("That name is taken.")).toBeInTheDocument();
		expect(
			screen.getByRole("dialog", { name: "New environment" }),
		).toBeInTheDocument();
	});

	it("starts each opening empty and unticked, with a fresh idempotency key", async () => {
		const keys: string[] = [];
		stubApi({
			"POST /projects/:projectId/environments": (request) => {
				keys.push(request.headers.get("Idempotency-Key") ?? "");
				return Response.json(
					{ code: "conflict", detail: "no", status: 409 },
					{ status: 409 },
				);
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<EnvHost />);
		await user.type(await screen.findByLabelText("Name"), "a");
		await user.click(screen.getByRole("checkbox", { name: /Production/ }));
		await user.click(envCreate());
		await screen.findByText("no");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		await user.click(screen.getByRole("button", { name: "Reopen" }));
		expect(await screen.findByLabelText("Name")).toHaveValue("");
		expect(
			screen.getByRole("checkbox", { name: /Production/ }),
		).not.toBeChecked();
		await user.type(screen.getByLabelText("Name"), "b");
		await user.click(envCreate());
		await waitFor(() => expect(keys).toHaveLength(2));
		expect(keys[1]).not.toBe(keys[0]);
	});

	it("does not post an empty name on Enter", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		await renderWithProviders(<EnvHost />);
		await user.type(await screen.findByLabelText("Name"), "{Enter}");
		expect(api.calls).toHaveLength(0);
	});
});
