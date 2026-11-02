import { describe, expect, mock, test } from "bun:test";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { ConfigServiceProvider } from "#/features/project-config";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import {
	CategoryHeadProvider,
	Section,
	SectionBody,
	SelectRow,
	SettingRow,
	TextRow,
} from "./fields";

const category = {
	label: "Networking",
	description: "How people and other services reach this service.",
};

/** A config source in which nebula.toml owns `fields`. */
const owned = (...fields: string[]) => ({
	body: {
		managed: true,
		state: "in_sync",
		origin: "git",
		repo: "acme/shop",
		branch: "main",
		path: "nebula.toml",
		commit: "3f9c2a1deadbeef",
		fields: fields.map((field) => ({ field, state: "in_sync" })),
		pending: [],
	},
});

describe("Section", () => {
	test("is a labelled region with a heading, a description and its rows", () => {
		render(
			<Section title="Identity" description="Who this service is.">
				<p>row one</p>
			</Section>,
		);
		const region = screen.getByRole("region", { name: "Identity" });
		expect(region).toHaveAttribute("data-anchor", "identity");
		expect(
			within(region).getByRole("heading", { level: 3, name: "Identity" }),
		).toBeInTheDocument();
		expect(
			within(region).getByText("Who this service is."),
		).toBeInTheDocument();
		expect(within(region).getByText("row one")).toBeInTheDocument();
	});

	test("the anchor is the slug of the title, so search can find it", () => {
		render(
			<Section title="Port and access (TCP)">
				<p>x</p>
			</Section>,
		);
		expect(screen.getByRole("region")).toHaveAttribute(
			"data-anchor",
			"port-and-access-tcp-",
		);
	});

	test("with neither title nor description to show, no head is drawn", () => {
		render(
			<CategoryHeadProvider category={category}>
				<Section title="Networking" description={category.description}>
					<p>row</p>
				</Section>
			</CategoryHeadProvider>,
		);
		const region = screen.getByRole("region", { name: "Networking" });
		expect(within(region).queryByRole("heading")).toBeNull();
		expect(within(region).queryByText(category.description)).toBeNull();
		expect(within(region).getByText("row")).toBeInTheDocument();
	});

	test("a title that repeats the category is dropped but a different description stays", () => {
		render(
			<CategoryHeadProvider category={category}>
				<Section title="Networking" description="Ports and domains.">
					<p>row</p>
				</Section>
			</CategoryHeadProvider>,
		);
		expect(screen.queryByRole("heading", { name: "Networking" })).toBeNull();
		expect(screen.getByText("Ports and domains.")).toBeInTheDocument();
	});

	test("a different title keeps its heading under a category", () => {
		render(
			<CategoryHeadProvider category={category}>
				<Section title="Tailnet" description={category.description}>
					<p>row</p>
				</Section>
			</CategoryHeadProvider>,
		);
		expect(
			screen.getByRole("heading", { level: 3, name: "Tailnet" }),
		).toBeInTheDocument();
		// The description repeats the category's line, so it is dropped.
		expect(screen.queryByText(category.description)).toBeNull();
	});

	test("an aside keeps the heading even when the title repeats the category", () => {
		render(
			<CategoryHeadProvider category={category}>
				<Section title="Networking" aside="Applies now">
					<p>row</p>
				</Section>
			</CategoryHeadProvider>,
		);
		const heading = screen.getByRole("heading", { level: 3 });
		expect(heading).toHaveTextContent("Networking");
		expect(heading).toHaveTextContent("Applies now");
	});

	test("hidden stays mounted but is not shown", () => {
		render(
			<Section title="Secret" hidden>
				<input aria-label="kept" defaultValue="typed" />
			</Section>,
		);
		const region = screen.getByRole("region", { name: "Secret", hidden: true });
		expect(region).toHaveClass("hidden");
		expect(screen.getByLabelText("kept")).toHaveValue("typed");
	});

	test("a field group the nebula.toml owns is marked in the heading", async () => {
		stubApi({ "GET /services/s1/config-source": owned("scale.replicas") });
		await renderWithProviders(
			<ConfigServiceProvider serviceId="s1">
				<Section title="Scaling" field="scale">
					<p>row</p>
				</Section>
			</ConfigServiceProvider>,
		);
		expect(await screen.findByText("Set by nebula.toml")).toBeInTheDocument();
	});

	test("a field the file does not own gets no mark", async () => {
		const api = stubApi({
			"GET /services/s1/config-source": owned("config.port"),
		});
		await renderWithProviders(
			<ConfigServiceProvider serviceId="s1">
				<Section title="Scaling" field="scale">
					<p>row</p>
				</Section>
			</ConfigServiceProvider>,
		);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(screen.queryByText("Set by nebula.toml")).toBeNull();
	});
});

describe("SectionBody", () => {
	test("wraps free-form content and carries the search anchor", () => {
		render(
			<SectionBody anchor="compose" className="extra">
				<p>content</p>
			</SectionBody>,
		);
		const body = screen.getByText("content").parentElement;
		expect(body).toHaveAttribute("data-anchor", "compose");
		expect(body).toHaveClass("extra");
	});
});

describe("SettingRow", () => {
	test("the label names its control and the description sits beside it", () => {
		render(
			<SettingRow
				label="Port"
				htmlFor="the-port"
				description="Where it listens."
			>
				<input id="the-port" />
			</SettingRow>,
		);
		expect(screen.getByLabelText("Port")).toHaveAttribute("id", "the-port");
		expect(screen.getByText("Where it listens.")).toBeInTheDocument();
	});

	test("without htmlFor the label is plain text, not a <label>", () => {
		render(
			<SettingRow label="Volumes">
				<span>control</span>
			</SettingRow>,
		);
		expect(screen.getByText("Volumes").tagName).toBe("SPAN");
		expect(screen.getByText("control")).toBeInTheDocument();
	});

	test("a row with an anchor is findable by the settings search", () => {
		render(
			<SettingRow label="Env" anchor="env-row">
				<span>x</span>
			</SettingRow>,
		);
		expect(document.querySelector("[data-slot='setting-row']")).toHaveAttribute(
			"data-anchor",
			"env-row",
		);
	});

	test("info becomes a button named after the label, with the text as its description", () => {
		render(
			<SettingRow label="Pre-deploy command" info="Runs before the swap.">
				<span>x</span>
			</SettingRow>,
		);
		const tip = screen.getByRole("button", {
			name: "About pre-deploy command",
		});
		expect(tip).toHaveAccessibleDescription("Runs before the swap.");
	});

	test("info on a label that is not a string falls back to a generic name", () => {
		render(
			<SettingRow label={<em>Fancy</em>} info="Explained.">
				<span>x</span>
			</SettingRow>,
		);
		expect(
			screen.getByRole("button", { name: "More information" }),
		).toBeInTheDocument();
	});

	test("without info there is no tip button", () => {
		render(
			<SettingRow label="Plain">
				<span>x</span>
			</SettingRow>,
		);
		expect(screen.queryByRole("button")).toBeNull();
	});

	test("a field the file owns marks the row", async () => {
		stubApi({ "GET /services/s1/config-source": owned("config.port") });
		await renderWithProviders(
			<ConfigServiceProvider serviceId="s1">
				<SettingRow label="Port" field="config.port">
					<span>x</span>
				</SettingRow>
			</ConfigServiceProvider>,
		);
		expect(await screen.findByText("Set by nebula.toml")).toBeInTheDocument();
	});
});

function TextHarness(
	props: Partial<React.ComponentProps<typeof TextRow>> & { initial?: string },
) {
	const { initial = "", ...rest } = props;
	const [value, setValue] = useState(initial);
	return (
		<TextRow id="f" label="Field" value={value} onChange={setValue} {...rest} />
	);
}

describe("TextRow", () => {
	test("typing reports each new value", async () => {
		const user = userEvent.setup();
		const onChange = mock((_value: string) => {});
		render(<TextRow id="f" label="Field" value="" onChange={onChange} />);
		await user.type(screen.getByLabelText("Field"), "ab");
		expect(onChange.mock.calls).toEqual([["a"], ["b"]]);
	});

	test("shows the value it is given and follows it", async () => {
		const user = userEvent.setup();
		render(<TextHarness initial="start" />);
		const input = screen.getByLabelText("Field");
		expect(input).toHaveValue("start");
		await user.type(input, "!");
		expect(input).toHaveValue("start!");
	});

	test("an error is announced: invalid, described by its message", () => {
		render(<TextHarness error="Port must be a number." />);
		const input = screen.getByLabelText("Field");
		expect(input).toHaveAttribute("aria-invalid", "true");
		expect(input).toHaveAccessibleDescription("Port must be a number.");
	});

	test("without an error the field is valid and undescribed", () => {
		render(<TextHarness />);
		const input = screen.getByLabelText("Field");
		expect(input).toHaveAttribute("aria-invalid", "false");
		expect(input).not.toHaveAccessibleDescription();
	});

	test("a number row is a text input with a decimal keypad", () => {
		render(<TextHarness type="number" />);
		const input = screen.getByLabelText("Field");
		expect(input).toHaveAttribute("type", "text");
		expect(input).toHaveAttribute("inputmode", "decimal");
	});

	test("a password row hides what is typed, and a text row does not", () => {
		const { unmount } = render(<TextHarness type="password" />);
		expect(screen.getByLabelText("Field")).toHaveAttribute("type", "password");
		unmount();
		render(<TextHarness />);
		expect(screen.getByLabelText("Field")).toHaveAttribute("type", "text");
		expect(screen.getByLabelText("Field")).not.toHaveAttribute("inputmode");
	});

	test("disabled, placeholder, autocomplete and description are passed on", () => {
		render(
			<TextHarness
				disabled
				placeholder="3000"
				autoComplete="new-password"
				description="Where it listens."
			/>,
		);
		const input = screen.getByLabelText("Field");
		expect(input).toBeDisabled();
		expect(input).toHaveAttribute("placeholder", "3000");
		expect(input).toHaveAttribute("autocomplete", "new-password");
		expect(screen.getByText("Where it listens.")).toBeInTheDocument();
	});

	test("autocomplete is off by default", () => {
		render(<TextHarness />);
		expect(screen.getByLabelText("Field")).toHaveAttribute(
			"autocomplete",
			"off",
		);
	});

	test("an info tip on the row is reachable", () => {
		render(<TextHarness info="More about it." />);
		expect(
			screen.getByRole("button", { name: "About field" }),
		).toHaveAccessibleDescription("More about it.");
	});
});

const OPTIONS = [
	{ value: "a", label: "Alpha", text: "Alpha" },
	{ value: "b", label: <b>Beta</b>, text: "Beta" },
	{ value: "c", label: "Gamma", text: "Gamma", disabled: true },
];

function SelectHarness(
	props: Partial<React.ComponentProps<typeof SelectRow>> & {
		onPick?: (value: string) => void;
	},
) {
	const { onPick, ...rest } = props;
	const [value, setValue] = useState("a");
	return (
		<SelectRow
			id="sel"
			label="Choice"
			value={value}
			options={OPTIONS}
			onChange={(next) => {
				onPick?.(next);
				setValue(next);
			}}
			{...rest}
		/>
	);
}

describe("SelectRow", () => {
	test("the trigger shows the chosen option's plain text and is labelled by the row", () => {
		render(<SelectHarness />);
		const trigger = screen.getByRole("combobox", { name: "Choice" });
		expect(trigger).toHaveTextContent("Alpha");
	});

	test("choosing an option reports its value and updates the trigger", async () => {
		const user = userEvent.setup();
		const onPick = mock((_value: string) => {});
		render(<SelectHarness onPick={onPick} />);
		await user.click(screen.getByRole("combobox", { name: "Choice" }));
		const list = await screen.findByRole("listbox");
		expect(
			within(list)
				.getAllByRole("option")
				.map((o) => o.textContent),
		).toEqual(["Alpha", "Beta", "Gamma"]);
		await user.click(within(list).getByRole("option", { name: "Beta" }));
		expect(onPick.mock.calls).toEqual([["b"]]);
		await waitFor(() =>
			expect(
				screen.getByRole("combobox", { name: "Choice" }),
			).toHaveTextContent("Beta"),
		);
	});

	test("a disabled option can be seen but not chosen", async () => {
		const user = userEvent.setup();
		const onPick = mock((_value: string) => {});
		render(<SelectHarness onPick={onPick} />);
		await user.click(screen.getByRole("combobox", { name: "Choice" }));
		const gamma = await screen.findByRole("option", { name: "Gamma" });
		expect(gamma).toHaveAttribute("aria-disabled", "true");
		await user.click(gamma);
		expect(onPick).not.toHaveBeenCalled();
	});

	test("an error marks the trigger invalid and is shown", () => {
		render(<SelectHarness error="Pick a server." />);
		expect(screen.getByRole("combobox", { name: "Choice" })).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(screen.getByText("Pick a server.")).toBeInTheDocument();
	});

	test("without an error the trigger is valid", () => {
		render(<SelectHarness />);
		expect(screen.getByRole("combobox", { name: "Choice" })).toHaveAttribute(
			"aria-invalid",
			"false",
		);
	});

	test("an empty value shows the placeholder", () => {
		render(
			<SelectRow
				id="sel"
				label="Choice"
				value=""
				options={OPTIONS}
				placeholder="Pick one"
				onChange={() => {}}
			/>,
		);
		expect(screen.getByRole("combobox", { name: "Choice" })).toHaveTextContent(
			"Pick one",
		);
	});
});
