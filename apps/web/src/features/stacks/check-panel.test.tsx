import { describe, expect, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import { StackCheckPanel, ViolationList } from "./check-panel";
import type { StackCheckResult, StackCheckState } from "./model";
import { UNRESTRICTED_HINT } from "./model";

const RESULT: StackCheckResult = {
	components: [
		{ name: "web", index: 1, image: "nginx:1.27", ports: [80, 8080] },
		{ name: "worker", index: 0, image: "", ports: [] },
	],
	violations: [],
	notes: [],
	unrestricted: false,
	composeAvailable: true,
};

function state(overrides: Partial<StackCheckState> = {}): StackCheckState {
	return {
		empty: false,
		pending: false,
		result: RESULT,
		error: null,
		...overrides,
	};
}

/** The item at `index`; fails the test with a clear message when it is missing. */
function at<T>(items: readonly T[], index: number): T {
	const item = items[index];
	if (item === undefined) throw new Error(`expected an item at index ${index}`);
	return item;
}

describe("StackCheckPanel", () => {
	test("asks for a Compose file while the text is empty", () => {
		render(<StackCheckPanel state={state({ empty: true, result: null })} />);
		expect(
			screen.getByText(/Paste or upload a Compose file/),
		).toBeInTheDocument();
		expect(screen.queryByRole("status")).not.toBeInTheDocument();
	});

	test("says it is checking before the first answer arrives", () => {
		render(<StackCheckPanel state={state({ result: null, pending: true })} />);
		expect(screen.getByRole("status")).toHaveTextContent(
			"Checking the Compose file…",
		);
	});

	test("states the component count and a pass verdict, and lists the components", () => {
		render(<StackCheckPanel state={state()} />);
		expect(screen.getByRole("status")).toHaveTextContent(
			"2 components. The Compose file passes the checks.",
		);
		const list = screen.getByRole("list", { name: "Components" });
		const items = within(list).getAllByRole("listitem");
		expect(items).toHaveLength(2);
		expect(items[0]).toHaveTextContent("web");
		expect(items[0]).toHaveTextContent("nginx:1.27");
		expect(items[0]).toHaveTextContent(":80 :8080");
		expect(items[1]).toHaveTextContent("worker");
		expect(items[1]).toHaveTextContent("no image");
		expect(items[1]).toHaveTextContent("no ports");
	});

	test("uses the singular for one component", () => {
		render(
			<StackCheckPanel
				state={state({
					result: { ...RESULT, components: [at(RESULT.components, 0)] },
				})}
			/>,
		);
		expect(screen.getByRole("status")).toHaveTextContent(
			"1 component. The Compose file passes the checks.",
		);
	});

	test("a blocked file shows the verdict alone, without the component count", () => {
		render(
			<StackCheckPanel
				state={state({
					result: {
						...RESULT,
						violations: [
							{
								service: "web",
								key: "privileged",
								message: "Privileged containers are not supported.",
								kind: "unsupported",
							},
						],
					},
				})}
			/>,
		);
		expect(screen.getByRole("status")).toHaveTextContent(
			/^1 problem: 1 not supported\.$/,
		);
		expect(screen.getByRole("note")).toHaveTextContent("privileged");
	});

	test("shows a failed check instead of a verdict, with its message", () => {
		render(
			<StackCheckPanel
				state={state({
					result: null,
					error: "Compose file is not valid YAML.",
				})}
			/>,
		);
		expect(screen.getByRole("status")).toHaveTextContent(
			"The check did not run.",
		);
		expect(screen.getByRole("alert")).toHaveTextContent(
			"Compose file is not valid YAML.",
		);
	});

	test("marks the panel busy only while an answer is pending", () => {
		const { container, rerender } = render(
			<StackCheckPanel state={state({ pending: true })} />,
		);
		expect(container.firstElementChild).toHaveAttribute("aria-busy", "true");
		rerender(<StackCheckPanel state={state({ pending: false })} />);
		expect(container.firstElementChild).toHaveAttribute("aria-busy", "false");
	});

	test("the Checking indicator is announced only when an older answer is on screen", () => {
		const { rerender } = render(
			<StackCheckPanel state={state({ pending: true })} />,
		);
		expect(screen.getByText("Checking")).toHaveAttribute(
			"aria-hidden",
			"false",
		);
		rerender(<StackCheckPanel state={state({ pending: false })} />);
		expect(screen.getByText("Checking")).toHaveAttribute("aria-hidden", "true");
	});

	test("names the server when it is Unrestricted", () => {
		render(
			<StackCheckPanel
				state={state({ result: { ...RESULT, unrestricted: true } })}
				serverName="edge-1"
			/>,
		);
		expect(
			screen.getByText(
				"edge-1 is Unrestricted: restricted settings are allowed.",
			),
		).toBeInTheDocument();
	});

	test("does not claim Unrestricted without a server name or on a restricted server", () => {
		const { rerender } = render(
			<StackCheckPanel
				state={state({ result: { ...RESULT, unrestricted: true } })}
			/>,
		);
		expect(screen.queryByText(/is Unrestricted/)).not.toBeInTheDocument();
		rerender(<StackCheckPanel state={state()} serverName="edge-1" />);
		expect(screen.queryByText(/is Unrestricted/)).not.toBeInTheDocument();
	});

	test("warns when the server lacks the Compose plugin", () => {
		render(
			<StackCheckPanel
				state={state({ result: { ...RESULT, composeAvailable: false } })}
				serverName="edge-1"
			/>,
		);
		expect(screen.getByRole("note")).toHaveTextContent(
			"edge-1 does not have the Docker Compose plugin (2.24 or newer).",
		);
	});

	test("the plugin warning falls back to 'The server' without a name; unknown availability is silent", () => {
		const { rerender } = render(
			<StackCheckPanel
				state={state({ result: { ...RESULT, composeAvailable: false } })}
			/>,
		);
		expect(screen.getByRole("note")).toHaveTextContent(
			"The server does not have the Docker Compose plugin",
		);
		rerender(
			<StackCheckPanel
				state={state({ result: { ...RESULT, composeAvailable: null } })}
			/>,
		);
		expect(screen.queryByRole("note")).not.toBeInTheDocument();
	});

	test("lists the notes of the check", () => {
		render(
			<StackCheckPanel
				state={state({
					result: {
						...RESULT,
						notes: ["Build contexts are ignored.", "Second note."],
					},
				})}
			/>,
		);
		const notes = screen.getByRole("list", { name: "Notes" });
		expect(
			within(notes)
				.getAllByRole("listitem")
				.map((item) => item.textContent),
		).toEqual(["Build contexts are ignored.", "Second note."]);
	});
});

describe("ViolationList", () => {
	test("renders nothing without violations", () => {
		const { container } = render(
			<ViolationList result={{ violations: [], unrestricted: false }} />,
		);
		expect(container).toBeEmptyDOMElement();
	});

	const VIOLATIONS = [
		{
			service: "web",
			key: "cap_add",
			message: "cap_add needs admin.",
			kind: "restricted",
		},
		{
			service: "web",
			key: "privileged",
			message: "No privileged.",
			kind: "unsupported",
		},
		{
			service: "",
			key: "version",
			message: "Obsolete version.",
			kind: "unsupported",
		},
		{ service: "api", key: "pid", message: "pid host.", kind: "restricted" },
	];

	test("groups by service, the file itself first and then by name", () => {
		render(
			<ViolationList
				result={{ violations: VIOLATIONS, unrestricted: false }}
			/>,
		);
		const titles = screen
			.getAllByRole("note")
			.map((note) => note.getAttribute("data-service"));
		expect(titles).toEqual(["", "api", "web"]);
		expect(
			within(screen.getByRole("region", { name: "Problems" })).getByText(
				"Compose file",
			),
		).toBeInTheDocument();
	});

	test("tells not supported and restricted problems apart, with the admin hint", () => {
		render(
			<ViolationList
				result={{ violations: VIOLATIONS, unrestricted: false }}
			/>,
		);
		const web = screen
			.getAllByRole("note")
			.find((note) => note.getAttribute("data-service") === "web");
		expect(web).toBeDefined();
		expect(
			within(web as HTMLElement).getByText("Not supported"),
		).toBeInTheDocument();
		expect(
			within(web as HTMLElement).getByText("Restricted"),
		).toBeInTheDocument();
		expect(
			within(web as HTMLElement).getByText(UNRESTRICTED_HINT),
		).toBeInTheDocument();
		// The file-level group has only an unsupported problem: no hint there.
		const file = screen
			.getAllByRole("note")
			.find((note) => note.getAttribute("data-service") === "");
		expect(
			within(file as HTMLElement).queryByText(UNRESTRICTED_HINT),
		).toBeNull();
	});

	test("on an Unrestricted server a restricted problem reads as allowed and the hint goes away", () => {
		render(
			<ViolationList
				result={{
					violations: [at(VIOLATIONS, 0), at(VIOLATIONS, 3)],
					unrestricted: true,
				}}
			/>,
		);
		expect(screen.getAllByText("Allowed here")).toHaveLength(2);
		expect(screen.queryByText("Restricted")).not.toBeInTheDocument();
		expect(screen.queryByText(UNRESTRICTED_HINT)).not.toBeInTheDocument();
	});

	test("an unrecognised kind is treated as not supported", () => {
		render(
			<ViolationList
				result={{
					violations: [
						{ service: "web", key: "x", message: "m", kind: "weird" },
					],
					unrestricted: true,
				}}
			/>,
		);
		expect(screen.getByText("Not supported")).toBeInTheDocument();
	});

	test("a custom hint replaces the default one", () => {
		render(
			<ViolationList
				hint="Ask your admin"
				result={{ violations: [at(VIOLATIONS, 0)], unrestricted: false }}
			/>,
		);
		expect(screen.getByText("Ask your admin")).toBeInTheDocument();
		expect(screen.queryByText(UNRESTRICTED_HINT)).not.toBeInTheDocument();
	});
});
