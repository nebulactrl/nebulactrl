import { describe, expect, it } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef } from "react";
import type { Service } from "#/api/models";
import { DatabaseSuggestion } from "./database-suggestion";

const svc = (name: string, kind = "app") =>
	({
		id: name,
		name,
		kind,
		sourceType: "image",
		config: {},
	}) as unknown as Service;

function show(
	targets: string[],
	handlers: { onConnect?(): void; onDismiss?(): void } = {},
) {
	const connectRef = createRef<HTMLButtonElement>();
	render(
		<DatabaseSuggestion
			database={svc("postgres", "database")}
			targets={targets.map((name) => svc(name))}
			connectRef={connectRef}
			onConnect={handlers.onConnect ?? (() => {})}
			onDismiss={handlers.onDismiss ?? (() => {})}
		/>,
	);
	return connectRef;
}

describe("DatabaseSuggestion", () => {
	it("names the database and the single app that can reach it", () => {
		show(["web"]);
		const status = screen.getByRole("status");
		expect(status).toHaveTextContent(
			"postgres is not wired in. web can reach it.",
		);
	});

	it("joins two apps with 'and'", () => {
		show(["web", "worker"]);
		expect(screen.getByRole("status")).toHaveTextContent(
			"web and worker can reach it.",
		);
	});

	it("shortens a longer list to two names and a count", () => {
		show(["web", "worker", "cron", "admin"]);
		expect(screen.getByRole("status")).toHaveTextContent(
			"web, worker and 2 more can reach it.",
		);
	});

	it("gives the parent the Connect button to anchor on, and reports clicks", async () => {
		const user = userEvent.setup();
		let connects = 0;
		const ref = show(["web"], { onConnect: () => (connects += 1) });
		const connect = screen.getByRole<HTMLButtonElement>("button", {
			name: "Connect",
		});
		expect(ref.current).toBe(connect);
		await user.click(connect);
		expect(connects).toBe(1);
	});

	it("dismisses through the labelled cross", async () => {
		const user = userEvent.setup();
		let dismissed = 0;
		show(["web"], { onDismiss: () => (dismissed += 1) });
		await user.click(
			screen.getByRole("button", {
				name: "Dismiss the suggestion to connect postgres",
			}),
		);
		expect(dismissed).toBe(1);
	});
});
