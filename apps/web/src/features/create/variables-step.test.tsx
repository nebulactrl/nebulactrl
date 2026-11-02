import { describe, expect, mock, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ExampleChoices } from "#/features/variables/env-example";
import { scanBody, suggested } from "./fixtures";
import type { EnvPreview } from "./use-env-preview";
import { EnvPreviewNote, VariablesStep } from "./variables-step";

function preview(over: Partial<EnvPreview> = {}): EnvPreview {
	return { loading: false, scan: undefined, items: [], ...over };
}

describe("EnvPreviewNote", () => {
	test("says it is looking for .env.example while the repository is read (after the loader's short delay)", async () => {
		render(<EnvPreviewNote preview={preview({ loading: true })} />);
		expect(screen.queryByText("Looking for .env.example")).toBeNull();
		expect(await screen.findByText("Looking for .env.example")).toBeVisible();
	});

	test("says nothing before there is an answer", () => {
		const { container } = render(<EnvPreviewNote preview={preview()} />);
		expect(container).toBeEmptyDOMElement();
	});

	test("an unreadable repository says why, in a status line", () => {
		render(
			<EnvPreviewNote
				preview={preview({
					scan: scanBody({
						status: "unavailable",
						message: "no access to the repository",
						items: null,
					}),
				})}
			/>,
		);
		expect(screen.getByRole("status")).toHaveTextContent(
			"The example files could not be read: no access to the repository",
		);
	});

	test("a repository without example keys says nothing", () => {
		const { container } = render(
			<EnvPreviewNote
				preview={preview({
					scan: scanBody({ status: "none", items: null, files: null }),
					items: [],
				})}
			/>,
		);
		expect(container).toBeEmptyDOMElement();
	});

	test("counts the keys and names the files they came from", () => {
		const items = [suggested({ key: "A" }), suggested({ key: "B" })];
		render(
			<EnvPreviewNote
				preview={preview({
					scan: scanBody({
						items,
						files: [
							{ path: ".env.example", keys: 1 },
							{ path: "api/.env.sample", keys: 1 },
						],
					}),
					items,
				})}
			/>,
		);
		expect(screen.getByRole("status")).toHaveTextContent(
			"2 keys in .env.example, api/.env.sample. You review them next.",
		);
	});

	test("one key reads in the singular", () => {
		const items = [suggested()];
		render(
			<EnvPreviewNote
				preview={preview({ scan: scanBody({ items }), items })}
			/>,
		);
		expect(screen.getByRole("status")).toHaveTextContent(
			"1 key in .env.example.",
		);
	});
});

describe("VariablesStep", () => {
	const items = [
		suggested({ key: "LOG_LEVEL", value: "info" }),
		suggested({
			key: "API_TOKEN",
			value: "",
			needsInput: true,
			proposedSecret: true,
			comment: "From the dashboard",
		}),
	];
	const choices: ExampleChoices = {
		LOG_LEVEL: { checked: true, value: "info" },
		API_TOKEN: { checked: true, value: "" },
	};

	function setup(
		over: Partial<Parameters<typeof VariablesStep>[0]> = {},
		onChoices = mock((_next: ExampleChoices) => {}),
	) {
		render(
			<VariablesStep
				scan={scanBody({ items })}
				items={items}
				choices={choices}
				onChoices={onChoices}
				{...over}
			/>,
		);
		return onChoices;
	}

	test("lists every key, the one that needs a value first, with its hint", () => {
		setup();
		const list = screen.getByRole("list", { name: "Keys from .env.example" });
		const rows = list.querySelectorAll("li");
		expect(rows).toHaveLength(2);
		expect(rows[0]).toHaveTextContent("API_TOKEN");
		expect(rows[0]).toHaveTextContent("Needs a value");
		expect(rows[0]).toHaveTextContent("From the dashboard");
		expect(rows[1]).toHaveTextContent("LOG_LEVEL");
		expect(rows[1]).toHaveTextContent("info");
	});

	test("summarises how many are selected and how many still lack a value", () => {
		setup();
		expect(screen.getByText(/2 of 2 selected/)).toHaveTextContent(
			"2 of 2 selected · 1 without a value",
		);
	});

	test("unchecking a key lowers the selected count and drops it from 'without a value'", () => {
		setup({
			choices: {
				...choices,
				API_TOKEN: { checked: false, value: "" },
			},
		});
		expect(screen.getByText(/of 2 selected/)).toHaveTextContent(
			"1 of 2 selected",
		);
		expect(screen.getByText(/of 2 selected/)).not.toHaveTextContent(
			"without a value",
		);
	});

	test("names the keys NebulaCtrl sets itself, singular and plural", () => {
		const { unmount } = render(
			<VariablesStep
				scan={scanBody({
					items,
					skipped: [{ key: "DATABASE_URL", reason: "provided" }],
				})}
				items={items}
				choices={choices}
				onChoices={() => {}}
			/>,
		);
		expect(
			screen.getByText(/DATABASE_URL is set by NebulaCtrl and left out\./),
		).toBeVisible();
		unmount();
		render(
			<VariablesStep
				scan={scanBody({
					items,
					skipped: [
						{ key: "DATABASE_URL", reason: "provided" },
						{ key: "PORT", reason: "provided" },
					],
				})}
				items={items}
				choices={choices}
				onChoices={() => {}}
			/>,
		);
		expect(
			screen.getByText(
				/DATABASE_URL, PORT are set by NebulaCtrl and left out\./,
			),
		).toBeVisible();
	});

	test("typing a value reports the new choices, keeping the other keys' decisions", async () => {
		const user = userEvent.setup();
		const onChoices = setup();
		await user.type(screen.getByLabelText("Value for API_TOKEN"), "s");
		expect(onChoices).toHaveBeenLastCalledWith({
			LOG_LEVEL: { checked: true, value: "info" },
			API_TOKEN: { checked: true, value: "s" },
		});
	});

	test("unchecking a key reports it unchecked", async () => {
		const user = userEvent.setup();
		const onChoices = setup();
		await user.click(screen.getByRole("checkbox", { name: "Add LOG_LEVEL" }));
		expect(onChoices).toHaveBeenLastCalledWith({
			LOG_LEVEL: { checked: false, value: "info" },
			API_TOKEN: { checked: true, value: "" },
		});
	});

	test("focuses the first key that needs a value", () => {
		setup();
		expect(screen.getByLabelText("Value for API_TOKEN")).toHaveFocus();
	});
});
