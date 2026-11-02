import { describe, expect, it } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { SharedVariableView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, type Overrides, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { SharedVariableDialog } from "./shared-variable-dialog";
import type { SharedOwner } from "./use-shared-mutations";

// The product's own reference syntax; `{{` is how user-event types one `{`.
const TYPED_OPEN = "$" + "{{{{";
const OPEN = "$" + "{{";

/** `value: undefined` is a secret: the server never returns its value. */
function shared(over: Overrides<SharedVariableView> = {}): SharedVariableView {
	return make("SharedVariableView", {
		key: "REGION",
		value: "eu-1",
		isSecret: false,
		computedSecret: false,
		comment: "",
		generated: [],
		inherited: false,
		layer: "project",
		overrides: false,
		set: true,
		source: "manual",
		updatedAt: "2026-01-01T00:00:00Z",
		usedBy: 0,
		version: 1,
		...over,
	});
}

const OK = make("SharedVariableList");
const PROJECT: SharedOwner = { layer: "project", id: "p1" };

function Harness({
	owner = PROJECT,
	target = "new",
	siblings = [],
	onClosed,
}: {
	owner?: SharedOwner;
	target?: SharedVariableView | "new";
	siblings?: SharedVariableView[];
	onClosed?: () => void;
}) {
	const [open, setOpen] = useState<SharedVariableView | "new" | null>(target);
	return (
		<SharedVariableDialog
			owner={owner}
			ownerName="every environment of this project"
			target={open}
			siblings={siblings}
			onClose={() => {
				setOpen(null);
				onClosed?.();
			}}
		/>
	);
}

const name = () => screen.getByLabelText("Name");
const value = () => screen.getByLabelText("Value");
const secret = () => screen.getByRole("checkbox", { name: "Secret" });

const putRoutes = (path = "/projects/:id/shared-variables") => ({
	[`PUT ${path}`]: { body: OK },
	"GET /environments/:id/services": { body: make("PageServiceView") },
});

describe("SharedVariableDialog: adding", () => {
	it("keeps Add locked until the name is a valid key, and explains a bad one", async () => {
		const user = userEvent.setup();
		stubApi({});
		await renderWithProviders(<Harness />);
		const add = await screen.findByRole("button", { name: "Add variable" });
		expect(
			screen.getByRole("heading", { name: "New shared variable" }),
		).toBeVisible();
		expect(add).toBeDisabled();

		await user.type(name(), "1BAD");
		expect(
			screen.getByText(/Use letters, digits and underscores/),
		).toBeInTheDocument();
		expect(name()).toHaveAttribute("aria-invalid", "true");
		expect(add).toBeDisabled();

		await user.clear(name());
		await user.type(name(), "GOOD_1");
		expect(
			screen.queryByText(/Use letters, digits and underscores/),
		).toBeNull();
		expect(add).toBeEnabled();
	});

	it("writes the trimmed name and note with the value, toasts and closes", async () => {
		const user = userEvent.setup();
		let closed = 0;
		const api = stubApi(putRoutes());
		await renderWithProviders(<Harness onClosed={() => closed++} />);
		await user.type(await screen.findByLabelText("Name"), "  API_HOST ");
		await user.type(value(), "api.example.com");
		await user.type(screen.getByLabelText("Note"), "  public host ");
		await user.click(screen.getByRole("button", { name: "Add variable" }));

		expect(await screen.findByText("Added API_HOST")).toBeInTheDocument();
		expect(
			screen.getByText(
				"No service changed yet: add a reference where it is needed.",
			),
		).toBeInTheDocument();
		const put = api.calls.find((call) => call.method === "PUT");
		expect(put?.path).toBe("/projects/p1/shared-variables");
		expect(put?.body).toEqual({
			variables: [
				{ key: "API_HOST", comment: "public host", value: "api.example.com" },
			],
		});
		expect(closed).toBe(1);
		expect(api.unmatched).toEqual([]);
	});

	it("flags the variable as secret in the write when the box is ticked", async () => {
		const user = userEvent.setup();
		const api = stubApi(putRoutes());
		await renderWithProviders(<Harness />);
		await user.type(await screen.findByLabelText("Name"), "TOKEN");
		await user.type(value(), "s3cret");
		await user.click(secret());
		await user.click(screen.getByRole("button", { name: "Add variable" }));
		await screen.findByText("Added TOKEN");
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			variables: [
				{ key: "TOKEN", isSecret: true, comment: "", value: "s3cret" },
			],
		});
	});

	it("lists the restaged services in the toast", async () => {
		const user = userEvent.setup();
		stubApi({
			"PUT /projects/:id/shared-variables": {
				body: make("SharedVariableList", {
					restaged: [
						{ serviceId: "s1", slug: "web" },
						{ serviceId: "s2", slug: "worker" },
					],
				}),
			},
			"GET /environments/:id/services": { body: make("PageServiceView") },
		});
		await renderWithProviders(<Harness />);
		await user.type(await screen.findByLabelText("Name"), "REGION");
		await user.click(screen.getByRole("button", { name: "Add variable" }));
		expect(
			await screen.findByText("Restaged: web, worker. Deploy them to apply."),
		).toBeInTheDocument();
	});

	it("writes a team variable to the team endpoint and describes the team. reference", async () => {
		const user = userEvent.setup();
		const api = stubApi(putRoutes("/team/shared-variables"));
		await renderWithProviders(<Harness owner={{ layer: "team" }} />);
		await user.type(await screen.findByLabelText("Name"), "ORG");
		expect(screen.getByText(/KEY=\$\{\{team\.KEY\}\}/)).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Add variable" }));
		await screen.findByText("Added ORG");
		expect(api.calls.find((c) => c.method === "PUT")?.path).toBe(
			"/team/shared-variables",
		);
	});

	it("shows the server's error and stays open", async () => {
		const user = userEvent.setup();
		let closed = 0;
		stubApi({
			"PUT /projects/:id/shared-variables": problem(409, {
				code: "conflict",
				title: "Conflict",
				detail: "PORT is reserved",
			}),
		});
		await renderWithProviders(<Harness onClosed={() => closed++} />);
		await user.type(await screen.findByLabelText("Name"), "PORT");
		await user.click(screen.getByRole("button", { name: "Add variable" }));
		expect(await screen.findByText(/PORT is reserved/)).toBeInTheDocument();
		expect(closed).toBe(0);
		expect(screen.getByRole("button", { name: "Add variable" })).toBeEnabled();
	});

	it("Cancel closes without writing", async () => {
		const user = userEvent.setup();
		let closed = 0;
		const api = stubApi({});
		await renderWithProviders(<Harness onClosed={() => closed++} />);
		await user.type(await screen.findByLabelText("Name"), "X");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(closed).toBe(1);
		expect(api.calls).toEqual([]);
	});

	it("Generate inserts the expression into the value and makes it secret", async () => {
		const user = userEvent.setup();
		const api = stubApi(putRoutes());
		await renderWithProviders(<Harness />);
		await user.type(await screen.findByLabelText("Name"), "SESSION");
		await user.click(screen.getByRole("button", { name: "Generate" }));
		await user.click(await screen.findByRole("button", { name: "Insert" }));
		expect(value()).toHaveValue(`${OPEN}secret(32)}}`);
		expect(secret()).toBeChecked();
		await user.click(screen.getByRole("button", { name: "Add variable" }));
		await screen.findByText("Added SESSION");
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			variables: [
				{
					key: "SESSION",
					isSecret: true,
					comment: "",
					value: `${OPEN}secret(32)}}`,
				},
			],
		});
	});
});

describe("SharedVariableDialog: editing", () => {
	it("opens a plain variable read-only by name with its value and note", async () => {
		const user = userEvent.setup();
		const api = stubApi(putRoutes());
		await renderWithProviders(
			<Harness target={shared({ comment: "where we run" })} />,
		);
		expect(
			await screen.findByRole("heading", { name: "Edit REGION" }),
		).toBeVisible();
		expect(name()).toHaveAttribute("readonly");
		expect(name()).toHaveValue("REGION");
		expect(value()).toHaveValue("eu-1");
		expect(screen.getByLabelText("Note")).toHaveValue("where we run");

		await user.clear(value());
		await user.type(value(), "us-2");
		await user.click(screen.getByRole("button", { name: "Save variable" }));
		expect(await screen.findByText("Updated REGION")).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			variables: [{ key: "REGION", comment: "where we run", value: "us-2" }],
		});
	});

	it("keeps a stored secret: no value in the write, secret box locked", async () => {
		const user = userEvent.setup();
		const api = stubApi(putRoutes());
		await renderWithProviders(
			<Harness
				target={shared({
					key: "SESSION",
					isSecret: true,
					value: undefined,
					comment: "old",
				})}
			/>,
		);
		expect(await screen.findByLabelText("Value")).toHaveValue("");
		expect(value()).toHaveAttribute(
			"placeholder",
			"Leave empty to keep the current value",
		);
		expect(secret()).toBeChecked();
		expect(secret()).toHaveAttribute("aria-disabled", "true");

		await user.clear(screen.getByLabelText("Note"));
		await user.type(screen.getByLabelText("Note"), "rotated by ops");
		await user.click(screen.getByRole("button", { name: "Save variable" }));
		await screen.findByText("Updated SESSION");
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			variables: [
				{ key: "SESSION", isSecret: true, comment: "rotated by ops" },
			],
		});
	});

	it("replaces a stored secret once a new value is typed, and the box unlocks", async () => {
		const user = userEvent.setup();
		const api = stubApi(putRoutes());
		await renderWithProviders(
			<Harness
				target={shared({ key: "SESSION", isSecret: true, value: undefined })}
			/>,
		);
		await user.type(await screen.findByLabelText("Value"), "fresh");
		expect(secret()).not.toHaveAttribute("aria-disabled", "true");
		await user.click(screen.getByRole("button", { name: "Save variable" }));
		await screen.findByText("Updated SESSION");
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			variables: [
				{ key: "SESSION", isSecret: true, comment: "", value: "fresh" },
			],
		});
	});

	it("can turn a stored secret into a plain value by unticking it with a new value", async () => {
		const user = userEvent.setup();
		const api = stubApi(putRoutes());
		await renderWithProviders(
			<Harness
				target={shared({ key: "SESSION", isSecret: true, value: undefined })}
			/>,
		);
		await user.type(await screen.findByLabelText("Value"), "visible");
		await user.click(secret());
		await user.click(screen.getByRole("button", { name: "Save variable" }));
		await screen.findByText("Updated SESSION");
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			variables: [
				{ key: "SESSION", isSecret: false, comment: "", value: "visible" },
			],
		});
	});
});

describe("SharedVariableDialog: reference autocomplete", () => {
	const siblings = [
		shared({ key: "ALPHA", layer: "project" }),
		shared({ key: "BETA", layer: "environment", computedSecret: true }),
		shared({ key: "REGION" }),
	];

	it("offers the other shared variables and functions, never itself or services", async () => {
		const user = userEvent.setup();
		stubApi({});
		await renderWithProviders(
			<Harness target={shared({ key: "REGION" })} siblings={siblings} />,
		);
		const field = await screen.findByLabelText("Value");
		await user.clear(field);
		await user.type(field, TYPED_OPEN);
		const list = await screen.findByRole("listbox", {
			name: "Variable references",
		});
		const options = within(list)
			.getAllByRole("option")
			.map((option) => option.textContent ?? "");
		expect(options.some((text) => text.startsWith("shared.ALPHA"))).toBe(true);
		expect(options.some((text) => text.startsWith("shared.BETA"))).toBe(true);
		expect(options.some((text) => text.includes("shared.REGION"))).toBe(false);
		expect(options.some((text) => text.startsWith("uuid()"))).toBe(true);
		expect(options.some((text) => text.includes("NEBULA_"))).toBe(false);
		// The layer and the derived secret flag show on the rows.
		expect(within(list).getByText("shared · project")).toBeInTheDocument();
		expect(within(list).getByText("secret")).toBeInTheDocument();
	});

	it("Enter inserts the highlighted shared reference and saves it as the value", async () => {
		const user = userEvent.setup();
		const api = stubApi(putRoutes());
		await renderWithProviders(<Harness siblings={siblings} />);
		await user.type(await screen.findByLabelText("Name"), "COPY");
		await user.type(value(), `${TYPED_OPEN}shared.al`);
		await user.keyboard("{Enter}");
		expect(value()).toHaveValue(`${OPEN}shared.ALPHA}}`);
		expect(screen.queryByRole("listbox")).toBeNull();
		await user.click(screen.getByRole("button", { name: "Add variable" }));
		await screen.findByText("Added COPY");
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			variables: [{ key: "COPY", comment: "", value: `${OPEN}shared.ALPHA}}` }],
		});
	});
});
