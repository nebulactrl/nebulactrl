import { describe, expect, mock, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DomainProcessTarget, effectiveProcess } from "./domain-process-target";
import type { DomainProcessOption } from "./model";

const WEB: DomainProcessOption = { name: "web", port: 3000, isDefault: true };
const API: DomainProcessOption = { name: "api", port: 4000, isDefault: false };
const ADMIN: DomainProcessOption = {
	name: "admin",
	port: 5000,
	isDefault: false,
};

describe("effectiveProcess", () => {
	test("keeps a pick that is still an option", () => {
		expect(effectiveProcess([WEB, API], "api")).toBe("api");
	});

	test("without a pick, the default process with a port serves", () => {
		expect(effectiveProcess([API, WEB], "")).toBe("web");
	});

	test("a stale pick falls back to the default process", () => {
		expect(effectiveProcess([API, WEB], "gone")).toBe("web");
	});

	test("without a default, the only process with a port serves", () => {
		expect(effectiveProcess([API], "")).toBe("api");
	});

	test("with several ports and no default the choice is the reader's", () => {
		expect(effectiveProcess([API, ADMIN], "")).toBe("");
	});

	test("without any process with a port there is nothing to choose", () => {
		expect(effectiveProcess([], "")).toBe("");
		expect(effectiveProcess([], "web")).toBe("");
	});
});

describe("DomainProcessTarget", () => {
	test("shows the chosen process and its own read-only port", () => {
		render(
			<DomainProcessTarget
				options={[WEB, API]}
				process="api"
				onProcess={() => {}}
			/>,
		);
		expect(
			screen.getByRole("combobox", { name: "Process the domain serves" }),
		).toHaveTextContent("api");
		const port = screen.getByLabelText("Container port");
		expect(port).toHaveValue("4000");
		expect(port).toHaveAttribute("readonly");
		expect(port).toBeEnabled();
	});

	test("with no process chosen the port is empty and disabled", () => {
		render(
			<DomainProcessTarget
				options={[WEB, API]}
				process=""
				onProcess={() => {}}
			/>,
		);
		expect(screen.getByLabelText("Container port")).toHaveValue("");
		expect(screen.getByLabelText("Container port")).toBeDisabled();
		expect(
			screen.getByRole("combobox", { name: "Process the domain serves" }),
		).toHaveTextContent("Process");
	});

	test("picking another process reports its name", async () => {
		const onProcess = mock((_name: string) => {});
		render(
			<DomainProcessTarget
				options={[WEB, API]}
				process="web"
				onProcess={onProcess}
			/>,
		);
		const user = userEvent.setup();
		await user.click(
			screen.getByRole("combobox", { name: "Process the domain serves" }),
		);
		await user.click(await screen.findByRole("option", { name: "api" }));
		expect(onProcess.mock.calls).toEqual([["api"]]);
	});

	test("without a process that has a port it says to give one a port, and offers nothing", () => {
		render(
			<DomainProcessTarget options={[]} process="" onProcess={() => {}} />,
		);
		expect(
			screen.getByText("Give a process a port before adding a domain."),
		).toBeInTheDocument();
		expect(
			screen.getByRole("combobox", { name: "Process the domain serves" }),
		).toBeDisabled();
	});

	test("disabled locks both the picker and the port", () => {
		render(
			<DomainProcessTarget
				options={[WEB]}
				process="web"
				onProcess={() => {}}
				disabled
			/>,
		);
		expect(
			screen.getByRole("combobox", { name: "Process the domain serves" }),
		).toBeDisabled();
		expect(screen.getByLabelText("Container port")).toBeDisabled();
		expect(
			screen.queryByText("Give a process a port before adding a domain."),
		).not.toBeInTheDocument();
	});
});
