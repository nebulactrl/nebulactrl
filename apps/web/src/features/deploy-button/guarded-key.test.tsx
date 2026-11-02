import { describe, expect, jest, mock, test } from "bun:test";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { GuardedKey, useGuard } from "./guarded-key";

function Harness({
	disabled,
	sent = false,
	onPress,
	onOutsideClick,
}: {
	disabled?: boolean;
	sent?: boolean;
	onPress(): void;
	onOutsideClick?(): void;
}) {
	const guard = useGuard();
	return (
		<div>
			<button type="button" onClick={onOutsideClick}>
				elsewhere
			</button>
			<GuardedKey
				guard={guard}
				label="Deploy · 2"
				sent={sent}
				{...(disabled === undefined ? {} : { disabled })}
				onPress={onPress}
			/>
			<span data-testid="guard">
				{guard.armed ? "armed" : "closed"}/{guard.sent ? "sent" : "idle"}
			</span>
			<button type="button" onClick={guard.resolved}>
				resolve
			</button>
		</div>
	);
}

// What a screen reader hears; the visible "Deploying" is decorative.
const announced = () =>
	screen.queryByText("Deploying", { selector: ".sr-only" });
const cover = () => screen.getByRole("button", { name: /production guard/ });
const key = () =>
	screen.getByRole("button", { name: "Deploy · 2", hidden: true });

describe("GuardedKey", () => {
	test("starts closed: the cover reads PRODUCTION and the key does nothing", () => {
		const onPress = mock(() => {});
		render(<Harness onPress={onPress} />);
		expect(
			screen.getByRole("group", { name: "Production deploy" }),
		).toBeInTheDocument();
		expect(cover()).toHaveAccessibleName("Lift production guard");
		expect(cover()).toHaveAttribute("aria-expanded", "false");
		expect(cover()).toHaveTextContent("Production");
		expect(key()).toHaveAttribute("inert");
		// A script's click reaches an inert key; the guard still refuses it.
		fireEvent.click(key());
		expect(onPress).not.toHaveBeenCalled();
		expect(screen.getByTestId("guard")).toHaveTextContent("closed/idle");
	});

	test("lifting the cover arms the key; pressing it sends once and closes the cover", async () => {
		const onPress = mock(() => {});
		const user = userEvent.setup();
		render(<Harness onPress={onPress} />);
		await user.click(cover());
		expect(cover()).toHaveAccessibleName("Lower production guard");
		expect(cover()).toHaveAttribute("aria-expanded", "true");
		expect(key()).not.toHaveAttribute("inert");
		expect(screen.getByTestId("guard")).toHaveTextContent("armed/idle");

		await user.click(key());
		expect(onPress).toHaveBeenCalledTimes(1);
		expect(screen.getByTestId("guard")).toHaveTextContent("closed/sent");
		expect(key()).toHaveAttribute("data-busy", "true");
		expect(announced()).toBeInTheDocument();

		// While sent the cover cannot be lifted again, so no second press.
		await user.click(cover());
		expect(screen.getByTestId("guard")).toHaveTextContent("closed/sent");
		fireEvent.click(key());
		expect(onPress).toHaveBeenCalledTimes(1);

		// The request resolving unlocks the guard.
		await user.click(screen.getByRole("button", { name: "resolve" }));
		expect(screen.getByTestId("guard")).toHaveTextContent("closed/idle");
		await user.click(cover());
		expect(screen.getByTestId("guard")).toHaveTextContent("armed/idle");
	});

	test("pressing the cover again lowers it", async () => {
		const user = userEvent.setup();
		render(<Harness onPress={() => {}} />);
		await user.click(cover());
		await user.click(cover());
		expect(screen.getByTestId("guard")).toHaveTextContent("closed/idle");
		expect(cover()).toHaveAttribute("aria-expanded", "false");
	});

	test("Escape lowers the guard and does not reach an enclosing handler", async () => {
		const outer = mock(() => {});
		const user = userEvent.setup();
		render(
			// biome-ignore lint/a11y/noStaticElementInteractions: a stand-in for a sheet's key handler
			<div onKeyDown={outer}>
				<Harness onPress={() => {}} />
			</div>,
		);
		// With the guard closed Escape passes through (a sheet may close).
		cover().focus();
		await user.keyboard("{Escape}");
		expect(outer).toHaveBeenCalledTimes(1);

		await user.click(cover());
		expect(screen.getByTestId("guard")).toHaveTextContent("armed/idle");
		outer.mockClear();
		await user.keyboard("{Escape}");
		expect(screen.getByTestId("guard")).toHaveTextContent("closed/idle");
		expect(outer).not.toHaveBeenCalled();
	});

	test("a pointer press outside lowers the guard", async () => {
		const user = userEvent.setup();
		render(<Harness onPress={() => {}} />);
		await user.click(cover());
		await user.click(screen.getByRole("button", { name: "elsewhere" }));
		expect(screen.getByTestId("guard")).toHaveTextContent("closed/idle");
	});

	test("focus leaving the key and cover lowers the guard", async () => {
		const user = userEvent.setup();
		render(<Harness onPress={() => {}} />);
		await user.click(cover());
		expect(screen.getByTestId("guard")).toHaveTextContent("armed/idle");
		await user.tab();
		await user.tab();
		await waitFor(() =>
			expect(screen.getByTestId("guard")).toHaveTextContent("closed/idle"),
		);
	});

	test("a lifted guard drops by itself after four seconds", () => {
		render(<Harness onPress={() => {}} />);
		jest.useFakeTimers();
		try {
			fireEvent.click(cover());
			expect(screen.getByTestId("guard")).toHaveTextContent("armed/idle");
			act(() => jest.advanceTimersByTime(3999));
			expect(screen.getByTestId("guard")).toHaveTextContent("armed/idle");
			act(() => jest.advanceTimersByTime(1));
			expect(screen.getByTestId("guard")).toHaveTextContent("closed/idle");
		} finally {
			jest.useRealTimers();
		}
	});

	test("with nothing to deploy the guard stays shut and says why", async () => {
		const onPress = mock(() => {});
		const user = userEvent.setup();
		render(<Harness disabled onPress={onPress} />);
		expect(cover()).toHaveAttribute("title", "Nothing to deploy");
		expect(key()).toBeDisabled();
		await user.click(cover());
		expect(screen.getByTestId("guard")).toHaveTextContent("closed/idle");
		fireEvent.click(key());
		expect(onPress).not.toHaveBeenCalled();
	});

	test("a deployment already running locks the cover and shows Deploying", async () => {
		const user = userEvent.setup();
		render(<Harness sent onPress={() => {}} />);
		expect(announced()).toBeInTheDocument();
		expect(cover()).toHaveAttribute("aria-disabled", "true");
		await user.click(cover());
		expect(screen.getByTestId("guard")).toHaveTextContent("closed/idle");
	});

	test("without a running deployment the status stays empty", () => {
		render(<Harness onPress={() => {}} />);
		expect(announced()).toBeNull();
		expect(key()).not.toHaveAttribute("data-busy");
	});
});
