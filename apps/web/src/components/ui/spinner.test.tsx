import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { Spinner } from "./spinner";

describe("Spinner", () => {
	test("is a status region announced as Loading unless it is given another label", () => {
		const { unmount } = render(<Spinner />);
		expect(screen.getByRole("status", { name: "Loading" })).toBeInTheDocument();
		unmount();
		render(<Spinner aria-label="Deploying" />);
		expect(
			screen.getByRole("status", { name: "Deploying" }),
		).toBeInTheDocument();
		expect(screen.queryByRole("status", { name: "Loading" })).toBeNull();
	});
});
