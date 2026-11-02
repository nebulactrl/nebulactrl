import { describe, expect, mock, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DomainTargetFields } from "./domain-target";
import type { StackComponent } from "./model";

const COMPONENTS: StackComponent[] = [
	{ name: "db", index: 0, ports: [5432] },
	{ name: "web", index: 1, ports: [80, 8080] },
	{ name: "worker", index: 2, ports: [] },
];

function setup(props: Partial<Parameters<typeof DomainTargetFields>[0]> = {}) {
	const onComponent = mock((_name: string) => {});
	const onPort = mock((_value: string) => {});
	render(
		<DomainTargetFields
			components={COMPONENTS}
			component=""
			port=""
			onComponent={onComponent}
			onPort={onPort}
			{...props}
		/>,
	);
	return { onComponent, onPort };
}

describe("DomainTargetFields", () => {
	test("until a component is chosen the port field is disabled", () => {
		setup();
		expect(screen.getByLabelText("Container port")).toBeDisabled();
		expect(screen.getByLabelText("Component the domain serves")).toBeEnabled();
	});

	test("with no components the whole picker is disabled", () => {
		setup({ components: [] });
		expect(screen.getByLabelText("Component the domain serves")).toBeDisabled();
	});

	test("disabled turns both fields off", () => {
		setup({ component: "db", disabled: true });
		expect(screen.getByLabelText("Component the domain serves")).toBeDisabled();
		expect(screen.getByLabelText("Container port")).toBeDisabled();
	});

	test("a component with exactly one declared port shows it as the default", () => {
		setup({ component: "db" });
		const port = screen.getByLabelText("Container port");
		expect(port).toBeEnabled();
		expect(port).toHaveAttribute("placeholder", "5432 (default)");
		expect(port).not.toHaveAttribute("list");
	});

	test("several declared ports are offered as suggestions and no default is implied", () => {
		const { container } = render(
			<DomainTargetFields
				components={COMPONENTS}
				component="web"
				port=""
				onComponent={() => {}}
				onPort={() => {}}
			/>,
		);
		const port = screen.getByLabelText("Container port");
		expect(port).toHaveAttribute("placeholder", "port");
		expect(port).toHaveAttribute("list", "stack-ports");
		const options = [
			...container.querySelectorAll("datalist#stack-ports option"),
		].map((option) => option.getAttribute("value"));
		expect(options).toEqual(["80", "8080"]);
	});

	test("a component declaring no port asks for one without suggestions", () => {
		setup({ component: "worker" });
		const port = screen.getByLabelText("Container port");
		expect(port).toHaveAttribute("placeholder", "port");
		expect(port).not.toHaveAttribute("list");
	});

	test("typing a port reports the text as typed, any port accepted", async () => {
		const user = userEvent.setup();
		const { onPort } = setup({ component: "web" });
		await user.type(screen.getByLabelText("Container port"), "9");
		expect(onPort).toHaveBeenLastCalledWith("9");
	});

	test("shows the chosen port value", () => {
		setup({ component: "web", port: "8080" });
		expect(screen.getByLabelText("Container port")).toHaveValue("8080");
	});

	test("picking another component reports it and clears the port", async () => {
		const user = userEvent.setup();
		const { onComponent, onPort } = setup({ component: "db", port: "5432" });
		await user.click(screen.getByLabelText("Component the domain serves"));
		await user.click(await screen.findByRole("option", { name: "web" }));
		expect(onComponent).toHaveBeenCalledWith("web");
		expect(onPort).toHaveBeenCalledWith("");
	});
});
