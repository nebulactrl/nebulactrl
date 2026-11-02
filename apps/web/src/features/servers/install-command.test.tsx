import { afterEach, describe, expect, mock, test } from "bun:test";
import { renderHook, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { EnrollmentToken } from "#/api/gen/types.gen";
import { renderWithProviders } from "#/test/render";
import { InstallCommandField, useEnrollmentExpired } from "./install-command";

const COMMAND =
	"curl -fsSL https://ctrl.example.com/install-agent.sh | sudo sh -s -- --url https://ctrl.example.com --token nct_enroll_abc";

const token = (expiresInMs: number): EnrollmentToken => ({
	expiresAt: new Date(Date.now() + expiresInMs).toISOString(),
	installCommand: COMMAND,
	// Only the command and expiry are read by these components.
	server: { id: "srv-1" } as EnrollmentToken["server"],
	token: "nct_enroll_abc",
});

const clipboard = Object.getOwnPropertyDescriptor(navigator, "clipboard");
afterEach(() => {
	if (clipboard) Object.defineProperty(navigator, "clipboard", clipboard);
	else Reflect.deleteProperty(navigator, "clipboard");
});

/** Replaces navigator.clipboard for one test (undefined = plain http, no API). */
function stubClipboard(
	writeText: ((text: string) => Promise<void>) | undefined,
) {
	Object.defineProperty(navigator, "clipboard", {
		value: writeText ? { writeText } : undefined,
		configurable: true,
	});
}

describe("InstallCommandField", () => {
	test("shows the command one flag per line in a read-only box", async () => {
		await renderWithProviders(
			<InstallCommandField enrollment={token(3_600_000)} />,
		);
		const box = screen.getByLabelText("Install command");
		expect(box).toHaveAttribute("readonly");
		expect(box).toHaveValue(
			[
				"curl -fsSL https://ctrl.example.com/install-agent.sh",
				"| sudo sh -s --",
				"--url https://ctrl.example.com",
				"--token nct_enroll_abc",
			].join("\n"),
		);
	});

	test("says the token works once and when it expires", async () => {
		await renderWithProviders(
			<InstallCommandField enrollment={token(90 * 60_000)} />,
		);
		expect(screen.getByText(/Needs Docker and a root shell/)).toHaveTextContent(
			"expires in 1h",
		);
		expect(screen.queryByText(/has expired/)).not.toBeInTheDocument();
	});

	test("an expired token says so and asks for a new one", async () => {
		await renderWithProviders(
			<InstallCommandField enrollment={token(-1_000)} />,
		);
		expect(
			screen.getByText("This command has expired. Ask for a new one."),
		).toBeInTheDocument();
		expect(screen.queryByText(/Needs Docker/)).not.toBeInTheDocument();
	});

	test("a token flips to expired by itself when its time runs out", async () => {
		await renderWithProviders(<InstallCommandField enrollment={token(150)} />);
		expect(
			screen.getByText(/Needs Docker and a root shell/),
		).toBeInTheDocument();
		expect(
			await screen.findByText(
				"This command has expired. Ask for a new one.",
				{},
				{ timeout: 5_000 },
			),
		).toBeInTheDocument();
		expect(screen.queryByText(/Needs Docker/)).not.toBeInTheDocument();
	});

	test("Copy puts the whole original command (not the wrapped one) on the clipboard and confirms", async () => {
		const writeText = mock(async (_text: string) => {});
		const user = userEvent.setup(); // installs its own clipboard; stub after it
		stubClipboard(writeText);
		await renderWithProviders(
			<InstallCommandField enrollment={token(3_600_000)} />,
		);
		await user.click(screen.getByRole("button", { name: /Copy/ }));
		expect(writeText).toHaveBeenCalledTimes(1);
		expect(writeText).toHaveBeenCalledWith(COMMAND);
		await waitFor(() =>
			expect(screen.getByRole("status")).toHaveTextContent("Copied"),
		);
		expect(screen.getByRole("button", { name: /Copied/ })).toBeInTheDocument();
	});

	test("when the browser refuses the clipboard it says to select and copy by hand", async () => {
		const user = userEvent.setup(); // installs its own clipboard; stub after it
		stubClipboard(async () => {
			throw new Error("denied");
		});
		await renderWithProviders(
			<InstallCommandField enrollment={token(3_600_000)} />,
		);
		await user.click(screen.getByRole("button", { name: /Copy/ }));
		expect(
			await screen.findByRole("button", { name: /Select and copy/ }),
		).toBeInTheDocument();
		expect(screen.getByRole("status")).toBeEmptyDOMElement();
	});

	test("without a clipboard API (plain http) it says to select and copy by hand", async () => {
		const user = userEvent.setup(); // installs its own clipboard; stub after it
		stubClipboard(undefined);
		await renderWithProviders(
			<InstallCommandField enrollment={token(3_600_000)} />,
		);
		await user.click(screen.getByRole("button", { name: /Copy/ }));
		expect(
			await screen.findByRole("button", { name: /Select and copy/ }),
		).toBeInTheDocument();
	});
});

describe("useEnrollmentExpired", () => {
	test("no token counts as expired", () => {
		const { result } = renderHook(() => useEnrollmentExpired(undefined));
		expect(result.current).toBe(true);
	});

	test("a live token is not expired, a past one is", () => {
		expect(
			renderHook(() => useEnrollmentExpired(token(60_000))).result.current,
		).toBe(false);
		expect(
			renderHook(() => useEnrollmentExpired(token(-60_000))).result.current,
		).toBe(true);
	});
});
