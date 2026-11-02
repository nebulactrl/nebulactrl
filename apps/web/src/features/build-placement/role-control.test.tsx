import { describe, expect, mock, test } from "bun:test";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithProviders } from "#/test/render";
import { roleConsequence } from "./model";
import { RoleControl } from "./role-control";

describe("RoleControl", () => {
	test("presses the current role and says what it means", async () => {
		await renderWithProviders(
			<RoleControl value="builds" onChange={() => {}} />,
		);
		expect(
			screen.getByRole("button", { name: "Builds", pressed: true }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Deployments", pressed: false }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Both", pressed: false }),
		).toBeInTheDocument();
		expect(screen.getByRole("status")).toHaveTextContent(
			roleConsequence("builds"),
		);
		expect(screen.getByRole("status")).toHaveTextContent(
			/no apps are placed here/i,
		);
	});

	test("reports the picked role", async () => {
		const onChange = mock((_role: string) => {});
		await renderWithProviders(
			<RoleControl value="deployments" onChange={onChange} />,
		);
		await userEvent.setup().click(screen.getByRole("button", { name: "Both" }));
		expect(onChange).toHaveBeenCalledTimes(1);
		expect(onChange).toHaveBeenCalledWith("both");
	});

	test("clicking the current role again does not report a change", async () => {
		const onChange = mock((_role: string) => {});
		await renderWithProviders(<RoleControl value="both" onChange={onChange} />);
		await userEvent.setup().click(screen.getByRole("button", { name: "Both" }));
		expect(onChange).not.toHaveBeenCalled();
	});
});
