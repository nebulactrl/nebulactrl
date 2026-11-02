import { describe, expect, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { HookField } from "./hook-field";
import { HOOK_TEMPLATES, type HookDraft } from "./model";

const EMPTY: HookDraft = {
	command: "",
	user: "",
	timeout: "300",
	continueOnError: false,
};

const FILLED: HookDraft = {
	command: "pg_dump -f /data/dump.sql",
	user: "postgres",
	timeout: "120",
	continueOnError: false,
};

/** Keeps the draft the way the config form does and records every change. */
function Harness({
	initial,
	templates,
	error,
	timeoutError,
	changes,
}: {
	initial: HookDraft;
	templates?: boolean;
	error?: string;
	timeoutError?: string;
	changes: HookDraft[];
}) {
	const [draft, setDraft] = useState(initial);
	return (
		<HookField
			label="Before backup"
			description="Runs inside the container."
			draft={draft}
			onChange={(next) => {
				changes.push(next);
				setDraft(next);
			}}
			error={error}
			timeoutError={timeoutError}
			templates={templates ?? false}
		/>
	);
}

describe("HookField", () => {
	test("with no command only the command line shows", () => {
		render(<Harness initial={EMPTY} changes={[]} />);
		expect(screen.getByLabelText("Before backup")).toHaveValue("");
		expect(screen.getByPlaceholderText("No command")).toBeVisible();
		expect(screen.queryByLabelText("Run as user")).toBeNull();
		expect(screen.queryByLabelText("Timeout (s)")).toBeNull();
		expect(screen.queryByText("If it fails")).toBeNull();
	});

	test("a whitespace-only command counts as none", () => {
		render(<Harness initial={{ ...EMPTY, command: "   " }} changes={[]} />);
		expect(screen.queryByLabelText("Run as user")).toBeNull();
		expect(screen.queryByText("If it fails")).toBeNull();
	});

	test("typing a command reveals the user, timeout and failure policy", async () => {
		const user = userEvent.setup();
		const changes: HookDraft[] = [];
		render(<Harness initial={EMPTY} changes={changes} />);
		await user.type(screen.getByLabelText("Before backup"), "x");
		expect(changes).toEqual([{ ...EMPTY, command: "x" }]);
		expect(screen.getByLabelText("Run as user")).toHaveValue("");
		expect(screen.getByLabelText("Timeout (s)")).toHaveValue("300");
		expect(screen.getByText("If it fails")).toBeVisible();
	});

	test("clearing the command hides the extra fields again and keeps the other values", async () => {
		const user = userEvent.setup();
		const changes: HookDraft[] = [];
		render(<Harness initial={FILLED} changes={changes} />);
		expect(screen.getByLabelText("Run as user")).toHaveValue("postgres");
		await user.clear(screen.getByLabelText("Before backup"));
		expect(changes.at(-1)).toEqual({ ...FILLED, command: "" });
		expect(screen.queryByLabelText("Run as user")).toBeNull();
		expect(screen.queryByLabelText("Timeout (s)")).toBeNull();
	});

	test("editing the user and the timeout changes only that field", async () => {
		const user = userEvent.setup();
		const changes: HookDraft[] = [];
		render(<Harness initial={FILLED} changes={changes} />);
		await user.type(screen.getByLabelText("Run as user"), "2");
		expect(changes.at(-1)).toEqual({ ...FILLED, user: "postgres2" });
		await user.clear(screen.getByLabelText("Timeout (s)"));
		await user.type(screen.getByLabelText("Timeout (s)"), "45");
		expect(changes.at(-1)).toEqual({
			...FILLED,
			user: "postgres2",
			timeout: "45",
		});
	});

	test("the failure policy toggles between stopping and continuing", async () => {
		const user = userEvent.setup();
		const changes: HookDraft[] = [];
		render(<Harness initial={FILLED} changes={changes} />);
		const group = screen.getByRole("group", {
			name: "Before backup: if it fails",
		});
		const stop = within(group).getByRole("button", { name: "Stop the backup" });
		const keepGoing = within(group).getByRole("button", {
			name: "Continue anyway",
		});
		expect(stop).toHaveAttribute("aria-pressed", "true");
		expect(keepGoing).toHaveAttribute("aria-pressed", "false");

		await user.click(keepGoing);
		expect(changes.at(-1)).toEqual({ ...FILLED, continueOnError: true });
		expect(keepGoing).toHaveAttribute("aria-pressed", "true");
		expect(stop).toHaveAttribute("aria-pressed", "false");

		await user.click(stop);
		expect(changes.at(-1)).toEqual({ ...FILLED, continueOnError: false });
	});

	test("an existing continue-on-error draft shows as Continue anyway", () => {
		render(
			<Harness initial={{ ...FILLED, continueOnError: true }} changes={[]} />,
		);
		expect(
			screen.getByRole("button", { name: "Continue anyway" }),
		).toHaveAttribute("aria-pressed", "true");
	});

	test("templates are offered only when asked for", () => {
		const { unmount } = render(<Harness initial={EMPTY} changes={[]} />);
		expect(screen.queryByText("Templates")).toBeNull();
		for (const t of HOOK_TEMPLATES)
			expect(screen.queryByRole("button", { name: t.label })).toBeNull();
		unmount();

		render(<Harness initial={EMPTY} templates changes={[]} />);
		expect(screen.getByText("Templates")).toBeVisible();
		for (const t of HOOK_TEMPLATES)
			expect(screen.getByRole("button", { name: t.label })).toBeVisible();
	});

	test("a template fills the command line, replacing what was there, and nothing else", async () => {
		const user = userEvent.setup();
		const changes: HookDraft[] = [];
		render(<Harness initial={FILLED} templates changes={changes} />);
		const sqlite = HOOK_TEMPLATES.find((t) => t.id === "sqlite");
		if (!sqlite) throw new Error("sqlite template missing");
		await user.click(screen.getByRole("button", { name: sqlite.label }));
		expect(changes).toEqual([{ ...FILLED, command: sqlite.command }]);
		expect(screen.getByLabelText("Before backup")).toHaveValue(sqlite.command);

		const valkey = HOOK_TEMPLATES.find((t) => t.id === "valkey");
		if (!valkey) throw new Error("valkey template missing");
		await user.click(screen.getByRole("button", { name: valkey.label }));
		expect(screen.getByLabelText("Before backup")).toHaveValue(valkey.command);
	});

	test("a template on an empty hook reveals the other fields", async () => {
		const user = userEvent.setup();
		render(<Harness initial={EMPTY} templates changes={[]} />);
		expect(screen.queryByLabelText("Timeout (s)")).toBeNull();
		await user.click(screen.getByRole("button", { name: "Valkey or Redis" }));
		expect(screen.getByLabelText("Timeout (s)")).toHaveValue("300");
	});

	test("a command error is shown and marks the command field invalid", () => {
		render(
			<Harness
				initial={{ ...EMPTY, command: "echo 'oops" }}
				error="A quote is not closed."
				changes={[]}
			/>,
		);
		expect(screen.getByText("A quote is not closed.")).toBeVisible();
		expect(screen.getByLabelText("Before backup")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(screen.getByLabelText("Timeout (s)")).not.toHaveAttribute(
			"aria-invalid",
		);
	});

	test("a timeout error is shown and marks the timeout field invalid", () => {
		render(
			<Harness
				initial={{ ...FILLED, timeout: "0" }}
				timeoutError="Enter 1 to 3600 seconds."
				changes={[]}
			/>,
		);
		expect(screen.getByText("Enter 1 to 3600 seconds.")).toBeVisible();
		expect(screen.getByLabelText("Timeout (s)")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(screen.getByLabelText("Before backup")).not.toHaveAttribute(
			"aria-invalid",
		);
	});

	test("no error text without errors", () => {
		render(<Harness initial={FILLED} changes={[]} />);
		expect(screen.queryByText(/A quote|Enter 1 to/)).toBeNull();
	});
});
