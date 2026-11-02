import { afterEach, describe, expect, it, jest, spyOn } from "bun:test";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { renderWithProviders } from "#/test/render";
import {
	ConfirmDialog,
	type ConfirmOptions,
	ConfirmProvider,
	useConfirm,
} from "./confirm-dialog";

/** A page with one button that asks, and a line reporting the answer. */
function Asker({ options }: { options: ConfirmOptions }) {
	const confirm = useConfirm();
	const [answer, setAnswer] = useState("none");
	return (
		<>
			<button
				type="button"
				onClick={async () => setAnswer(String(await confirm(options)))}
			>
				Ask
			</button>
			<output aria-label="answer">{answer}</output>
		</>
	);
}

const BASE: ConfirmOptions = {
	title: "Restart checkout-api?",
	body: "It will be down for a few seconds.",
	cta: "Restart",
};

/** Calls useConfirm and renders nothing. */
function Bare() {
	useConfirm();
	return null;
}

/** Asks two questions back to back and logs how each one resolved. */
function Twice() {
	const confirm = useConfirm();
	const [log, setLog] = useState<string[]>([]);
	return (
		<>
			<button
				type="button"
				onClick={() => {
					void confirm({ ...BASE, title: "First" }).then((v) =>
						setLog((l) => [...l, `first:${v}`]),
					);
					void confirm({ ...BASE, title: "Second" }).then((v) =>
						setLog((l) => [...l, `second:${v}`]),
					);
				}}
			>
				Ask twice
			</button>
			<output aria-label="log">{log.join(",")}</output>
		</>
	);
}

async function ask(options: Partial<ConfirmOptions> = {}) {
	const user = userEvent.setup();
	await renderWithProviders(<Asker options={{ ...BASE, ...options }} />);
	await user.click(screen.getByRole("button", { name: "Ask" }));
	await screen.findByRole("alertdialog");
	return user;
}

/**
 * A manual clock for the 1.3 s hold only: the hold button's timer is captured
 * instead of waited for, so the test fires or cancels it at will. (Bun's fake
 * timers leave userEvent hanging in later tests, so they are not used here.)
 */
const HOLD_MS = 1300;
const held = new Map<number, () => void>();
let nextHeldId = 1_000_000_000;

function captureHoldTimer() {
	const realSet = window.setTimeout.bind(window);
	const realClear = window.clearTimeout.bind(window);
	spyOn(window, "setTimeout").mockImplementation(((
		fn: () => void,
		ms?: number,
		...args: unknown[]
	) => {
		if (ms !== HOLD_MS) return realSet(fn, ms, ...args);
		const id = nextHeldId++;
		held.set(id, fn);
		return id;
	}) as unknown as typeof window.setTimeout);
	spyOn(window, "clearTimeout").mockImplementation(((id?: number) => {
		if (id !== undefined && held.has(id)) held.delete(id);
		else realClear(id);
	}) as unknown as typeof window.clearTimeout);
}

/** Lets the held timer fire; false when nothing was pending. */
async function elapseHold() {
	const [id, fn] = [...held][0] ?? [];
	if (id === undefined || !fn) return false;
	held.delete(id);
	await act(async () => fn());
	// waitFor mistakes the spied setTimeout for fake timers.
	jest.restoreAllMocks();
	return true;
}

afterEach(() => {
	held.clear();
	jest.restoreAllMocks();
});

const answer = () => screen.getByLabelText("answer");

describe("useConfirm", () => {
	it("explains how to mount the provider when used without one", () => {
		const error = console.error;
		console.error = () => {};
		try {
			expect(() => render(<Bare />)).toThrow(/needs a <ConfirmProvider>/);
		} finally {
			console.error = error;
		}
	});

	it("shows the question, the body and a Cancel button", async () => {
		await ask();
		const dialog = screen.getByRole("alertdialog");
		expect(dialog).toHaveTextContent("Restart checkout-api?");
		expect(dialog).toHaveTextContent("It will be down for a few seconds.");
		expect(screen.getByRole("button", { name: "Cancel" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Restart" })).toBeInTheDocument();
	});

	it("resolves true when the action is confirmed and closes the dialog", async () => {
		const user = await ask();
		await user.click(screen.getByRole("button", { name: "Restart" }));
		await waitFor(() => expect(answer()).toHaveTextContent("true"));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
	});

	it("resolves false when cancelled", async () => {
		const user = await ask();
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(answer()).toHaveTextContent("false"));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
	});

	it("resolves false when dismissed with Escape", async () => {
		const user = await ask();
		await user.keyboard("{Escape}");
		await waitFor(() => expect(answer()).toHaveTextContent("false"));
	});

	it("names the dismiss button when the action itself cancels something", async () => {
		await ask({ dismiss: "Keep deploying" });
		expect(
			screen.getByRole("button", { name: "Keep deploying" }),
		).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();
	});

	it("counts a superseded request as cancelled", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Twice />);
		await user.click(screen.getByRole("button", { name: "Ask twice" }));
		expect(await screen.findByRole("alertdialog")).toHaveTextContent("Second");
		await waitFor(() =>
			expect(screen.getByLabelText("log")).toHaveTextContent("first:false"),
		);
		await user.click(screen.getByRole("button", { name: "Restart" }));
		await waitFor(() =>
			expect(screen.getByLabelText("log")).toHaveTextContent(
				"first:false,second:true",
			),
		);
	});

	it("opens with the trash glyph for a delete", async () => {
		await ask({ glyph: "trash", title: "Delete service?" });
		expect(
			screen.getByRole("alertdialog", { name: "Delete service?" }),
		).toBeInTheDocument();
	});
});

describe("onConfirm", () => {
	it("keeps the dialog open and locked while the action runs, then resolves true", async () => {
		let finish: () => void = () => {};
		const onConfirm = jest.fn(
			() => new Promise<void>((resolve) => (finish = resolve)),
		);
		const user = await ask({ onConfirm });
		await user.click(screen.getByRole("button", { name: "Restart" }));
		expect(onConfirm).toHaveBeenCalledTimes(1);
		expect(screen.getByRole("alertdialog")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: /Restart/ })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
		// A running action cannot be abandoned half-way.
		await user.keyboard("{Escape}");
		expect(screen.getByRole("alertdialog")).toBeInTheDocument();
		expect(answer()).toHaveTextContent("none");

		await act(async () => finish());
		await waitFor(() => expect(answer()).toHaveTextContent("true"));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
	});

	it("shows the failure under the body and lets the reader try again", async () => {
		const onConfirm = jest
			.fn<() => Promise<void>>()
			.mockRejectedValueOnce(new Error("server unreachable"))
			.mockResolvedValueOnce();
		const user = await ask({ onConfirm });
		await user.click(screen.getByRole("button", { name: "Restart" }));
		expect(await screen.findByText("server unreachable")).toBeInTheDocument();
		expect(screen.getByRole("alertdialog")).toBeInTheDocument();
		expect(answer()).toHaveTextContent("none");
		// Unlocked again.
		expect(screen.getByRole("button", { name: "Restart" })).toBeEnabled();

		await user.click(screen.getByRole("button", { name: "Restart" }));
		await waitFor(() => expect(answer()).toHaveTextContent("true"));
		expect(onConfirm).toHaveBeenCalledTimes(2);
	});

	it("falls back to a generic message for a non-Error rejection", async () => {
		const onConfirm = jest.fn(() => Promise.reject({ code: 500 }));
		const user = await ask({ onConfirm });
		await user.click(screen.getByRole("button", { name: "Restart" }));
		expect(
			await screen.findByText("Something went wrong."),
		).toBeInTheDocument();
	});

	it("clears the previous error when the action is retried", async () => {
		let finish: () => void = () => {};
		const onConfirm = jest
			.fn<() => Promise<void>>()
			.mockRejectedValueOnce(new Error("first failure"))
			.mockImplementationOnce(() => new Promise<void>((r) => (finish = r)));
		const user = await ask({ onConfirm });
		await user.click(screen.getByRole("button", { name: "Restart" }));
		expect(await screen.findByText("first failure")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Restart" }));
		expect(screen.queryByText("first failure")).toBeNull();
		await act(async () => finish());
	});
});

describe("typeToConfirm", () => {
	it("keeps the action locked until the exact name is typed", async () => {
		const user = await ask({ typeToConfirm: "checkout-api" });
		const confirm = screen.getByRole("button", { name: "Restart" });
		expect(confirm).toBeDisabled();
		const input = screen.getByLabelText(/to confirm/);
		await user.type(input, "checkout");
		expect(confirm).toBeDisabled();
		await user.type(input, "-api");
		expect(confirm).toBeEnabled();
		await user.type(input, "x");
		expect(confirm).toBeDisabled();
	});

	it("confirms on Enter in the field once unlocked", async () => {
		const user = await ask({ typeToConfirm: "db" });
		const input = screen.getByLabelText(/to confirm/);
		await user.type(input, "d{Enter}");
		expect(answer()).toHaveTextContent("none");
		await user.type(input, "b{Enter}");
		await waitFor(() => expect(answer()).toHaveTextContent("true"));
	});

	it("starts each opening with an empty field", async () => {
		const user = await ask({ typeToConfirm: "db" });
		await user.type(screen.getByLabelText(/to confirm/), "db");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		await user.click(screen.getByRole("button", { name: "Ask" }));
		expect(await screen.findByLabelText(/to confirm/)).toHaveValue("");
	});
});

describe("blocked", () => {
	it("keeps the action locked while the body is still loading", async () => {
		await ask({ blocked: true });
		expect(screen.getByRole("button", { name: "Restart" })).toBeDisabled();
	});
});

describe("hold to confirm (danger)", () => {
	async function askDanger(options: Partial<ConfirmOptions> = {}) {
		const user = await ask({ danger: true, cta: "Delete service", ...options });
		return user;
	}

	it("replaces the click with a hold button named after the action", async () => {
		await askDanger();
		expect(
			screen.getByRole("button", { name: "Hold to delete service" }),
		).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Delete service" })).toBeNull();
	});

	it("starts the 1.3 s hold on press and confirms when it elapses", async () => {
		await askDanger();
		const hold = screen.getByRole("button", { name: "Hold to delete service" });
		captureHoldTimer();
		expect(held.size).toBe(0);
		fireEvent.pointerDown(hold, { button: 0, pointerId: 1 });
		expect(held.size).toBe(1);
		expect(answer()).toHaveTextContent("none");
		await elapseHold();
		await waitFor(() => expect(answer()).toHaveTextContent("true"));
	});

	it("does nothing when released early", async () => {
		await askDanger();
		const hold = screen.getByRole("button", { name: "Hold to delete service" });
		captureHoldTimer();
		fireEvent.pointerDown(hold, { button: 0, pointerId: 1 });
		expect(held.size).toBe(1);
		fireEvent.pointerUp(hold, { button: 0, pointerId: 1 });
		// The release cancelled the timer: there is nothing left to elapse.
		expect(held.size).toBe(0);
		expect(await elapseHold()).toBe(false);
		expect(answer()).toHaveTextContent("none");
		expect(screen.getByRole("alertdialog")).toBeInTheDocument();
	});

	it("does not let Enter in the type-to-confirm field skip the hold", async () => {
		const user = await askDanger({ typeToConfirm: "api" });
		await user.type(screen.getByLabelText(/to confirm/), "api{Enter}");
		expect(answer()).toHaveTextContent("none");
		expect(screen.getByRole("alertdialog")).toBeInTheDocument();
	});

	it("does not start a hold while the typed name is wrong", async () => {
		await askDanger({ typeToConfirm: "api" });
		const hold = screen.getByRole("button", { name: "Hold to delete service" });
		captureHoldTimer();
		fireEvent.pointerDown(hold, { button: 0, pointerId: 1 });
		expect(held.size).toBe(0);
		expect(answer()).toHaveTextContent("none");
	});

	it("starts the hold once the typed name matches", async () => {
		const user = await askDanger({ typeToConfirm: "api" });
		await user.type(screen.getByLabelText(/to confirm/), "api");
		const hold = screen.getByRole("button", { name: "Hold to delete service" });
		captureHoldTimer();
		fireEvent.pointerDown(hold, { button: 0, pointerId: 1 });
		expect(held.size).toBe(1);
		await elapseHold();
		await waitFor(() => expect(answer()).toHaveTextContent("true"));
	});

	it("is a plain click button when hold is switched off", async () => {
		const user = await askDanger({ hold: false });
		await user.click(screen.getByRole("button", { name: "Delete service" }));
		await waitFor(() => expect(answer()).toHaveTextContent("true"));
	});

	it("holds without danger when hold is asked for", async () => {
		await ask({ hold: true });
		expect(
			screen.getByRole("button", { name: "Hold to restart" }),
		).toBeInTheDocument();
	});

	it("drops a trailing ellipsis from the hold label", async () => {
		await askDanger({ cta: "Wipe…" });
		expect(
			screen.getByRole("button", { name: "Hold to wipe" }),
		).toBeInTheDocument();
	});
});

describe("guarded (production)", () => {
	it("confirms only after the cover is lifted and the key pressed", async () => {
		const user = await ask({ guarded: true, cta: "Deploy" });
		expect(
			screen.getByRole("group", { name: "Production deploy" }),
		).toBeInTheDocument();
		await user.click(
			screen.getByRole("button", { name: "Lift production guard" }),
		);
		await user.click(screen.getByRole("button", { name: "Deploy" }));
		await waitFor(() => expect(answer()).toHaveTextContent("true"));
	});

	it("does not confirm from a scripted click while the cover is down", async () => {
		await ask({ guarded: true, cta: "Deploy" });
		fireEvent.click(screen.getByText("Deploy"));
		expect(answer()).toHaveTextContent("none");
	});

	it("keeps the key locked until the typed name matches", async () => {
		const user = await ask({
			guarded: true,
			cta: "Deploy",
			typeToConfirm: "prod",
		});
		const cover = screen.getByRole("button", { name: "Lift production guard" });
		expect(cover).toHaveAttribute("aria-disabled", "true");
		await user.type(screen.getByLabelText(/to confirm/), "prod");
		expect(cover).not.toHaveAttribute("aria-disabled");
	});
});

describe("ConfirmDialog (controlled)", () => {
	it("renders nothing while closed", async () => {
		await renderWithProviders(
			<ConfirmDialog {...BASE} open={false} onOpenChange={() => {}} />,
		);
		expect(screen.queryByRole("alertdialog")).toBeNull();
	});

	it("reports onConfirmed then onOpenChange(false) when confirmed", async () => {
		const calls: string[] = [];
		const user = userEvent.setup();
		await renderWithProviders(
			<ConfirmDialog
				{...BASE}
				open
				onOpenChange={(open) => calls.push(`open:${open}`)}
				onConfirmed={() => calls.push("confirmed")}
			/>,
		);
		await user.click(await screen.findByRole("button", { name: "Restart" }));
		await waitFor(() => expect(calls).toEqual(["confirmed", "open:false"]));
	});

	it("reports onOpenChange(false) without onConfirmed when cancelled", async () => {
		const onOpenChange = jest.fn();
		const onConfirmed = jest.fn();
		const user = userEvent.setup();
		await renderWithProviders(
			<ConfirmDialog
				{...BASE}
				open
				onOpenChange={onOpenChange}
				onConfirmed={onConfirmed}
			/>,
		);
		await user.click(await screen.findByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect(onConfirmed).not.toHaveBeenCalled();
	});
});

describe("ConfirmProvider", () => {
	it("renders its children with no dialog until asked", async () => {
		await renderWithProviders(
			<ConfirmProvider>
				<p>inside</p>
			</ConfirmProvider>,
		);
		expect(screen.getByText("inside")).toBeInTheDocument();
		expect(screen.queryByRole("alertdialog")).toBeNull();
	});
});
