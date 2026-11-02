import { describe, expect, test } from "bun:test";
import { useRouterState } from "@tanstack/react-router";
import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import { renderWithProviders } from "#/test/render";
import { useDebugParam, useDebugShortcut } from "./use-debug-param";

function Param() {
	const { deploymentId, open, close } = useDebugParam();
	const search = useRouterState({ select: (s) => s.location.searchStr });
	return (
		<div>
			<output aria-label="deployment">{deploymentId ?? "none"}</output>
			<output aria-label="search">{search}</output>
			<button type="button" onClick={() => open("dep-9")}>
				open
			</button>
			<button type="button" onClick={close}>
				close
			</button>
		</div>
	);
}

describe("useDebugParam", () => {
	test("reads the deployment from the address", async () => {
		await renderWithProviders(<Param />, { path: "/?debug=dep-3" });
		expect(screen.getByLabelText("deployment")).toHaveTextContent("dep-3");
	});

	test("no param means closed", async () => {
		await renderWithProviders(<Param />, { path: "/" });
		expect(screen.getByLabelText("deployment")).toHaveTextContent("none");
	});

	test("an empty param means closed", async () => {
		await renderWithProviders(<Param />, { path: "/?debug=" });
		expect(screen.getByLabelText("deployment")).toHaveTextContent("none");
	});

	test("a non-string param (a number in the address) means closed", async () => {
		await renderWithProviders(<Param />, { path: "/?debug=123" });
		expect(screen.getByLabelText("deployment")).toHaveTextContent("none");
	});

	test("open writes the id next to the other search params", async () => {
		await renderWithProviders(<Param />, {
			path: "/?service=svc-1&tab=deployments",
		});

		fireEvent.click(screen.getByRole("button", { name: "open" }));

		await waitFor(() =>
			expect(screen.getByLabelText("deployment")).toHaveTextContent("dep-9"),
		);
		const search = screen.getByLabelText("search").textContent ?? "";
		expect(search).toContain("service=svc-1");
		expect(search).toContain("tab=deployments");
		expect(search).toContain("debug=dep-9");
	});

	test("close removes only the debug param", async () => {
		await renderWithProviders(<Param />, {
			path: "/?service=svc-1&debug=dep-3",
		});

		fireEvent.click(screen.getByRole("button", { name: "close" }));

		await waitFor(() =>
			expect(screen.getByLabelText("deployment")).toHaveTextContent("none"),
		);
		const search = screen.getByLabelText("search").textContent ?? "";
		expect(search).toContain("service=svc-1");
		expect(search).not.toContain("debug");
	});
});

function Changing({ seen }: { seen: string[] }) {
	const [label, setLabel] = useState("first");
	useDebugShortcut(() => seen.push(label));
	return (
		<button type="button" onClick={() => setLabel("second")}>
			change
		</button>
	);
}

function Shortcut({ onPress }: { onPress: () => void }) {
	useDebugShortcut(onPress);
	return (
		<div>
			<input aria-label="name" />
			<textarea aria-label="notes" />
			<div contentEditable suppressContentEditableWarning data-testid="rich">
				text
			</div>
		</div>
	);
}

async function renderShortcut() {
	let presses = 0;
	await renderWithProviders(
		<Shortcut
			onPress={() => {
				presses += 1;
			}}
		/>,
	);
	return () => presses;
}

describe("useDebugShortcut", () => {
	test("d runs the callback and prevents the default", async () => {
		const presses = await renderShortcut();

		const proceeded = fireEvent.keyDown(document.body, { key: "d" });

		expect(presses()).toBe(1);
		expect(proceeded).toBe(false);
	});

	test("an upper-case D (caps lock) works too, a shifted one does not", async () => {
		const presses = await renderShortcut();
		fireEvent.keyDown(document.body, { key: "D" });
		expect(presses()).toBe(1);
		fireEvent.keyDown(document.body, { key: "D", shiftKey: true });
		expect(presses()).toBe(1);
	});

	test("other keys do nothing", async () => {
		const presses = await renderShortcut();
		fireEvent.keyDown(document.body, { key: "x" });
		expect(presses()).toBe(0);
	});

	test.each(["metaKey", "ctrlKey", "altKey"] as const)(
		"%s+d is left to the browser",
		async (modifier) => {
			const presses = await renderShortcut();
			fireEvent.keyDown(document.body, { key: "d", [modifier]: true });
			expect(presses()).toBe(0);
		},
	);

	test("a held key does not repeat", async () => {
		const presses = await renderShortcut();
		fireEvent.keyDown(document.body, { key: "d", repeat: true });
		expect(presses()).toBe(0);
	});

	test("typing a d into a field does not open the panel", async () => {
		const presses = await renderShortcut();
		fireEvent.keyDown(screen.getByLabelText("name"), { key: "d" });
		fireEvent.keyDown(screen.getByLabelText("notes"), { key: "d" });
		fireEvent.keyDown(screen.getByTestId("rich"), { key: "d" });
		expect(presses()).toBe(0);
	});

	test("a key already handled elsewhere is ignored", async () => {
		const presses = await renderShortcut();
		const handled = new KeyboardEvent("keydown", {
			key: "d",
			cancelable: true,
			bubbles: true,
		});
		handled.preventDefault();
		act(() => {
			document.body.dispatchEvent(handled);
		});
		expect(presses()).toBe(0);
	});

	test("an open dialog or menu takes the key, and gives it back once closed", async () => {
		const presses = await renderShortcut();
		const menu = document.createElement("div");
		menu.setAttribute("role", "menu");
		menu.setAttribute("data-open", "");
		document.body.append(menu);

		fireEvent.keyDown(document.body, { key: "d" });
		expect(presses()).toBe(0);

		menu.remove();
		fireEvent.keyDown(document.body, { key: "d" });
		expect(presses()).toBe(1);
	});

	test("the debug panel itself (a sheet layered over) also silences it", async () => {
		const presses = await renderShortcut();
		const sheet = document.createElement("div");
		sheet.setAttribute("data-slot", "sheet-content");
		sheet.setAttribute("data-layer", "over");
		sheet.setAttribute("data-open", "");
		document.body.append(sheet);

		fireEvent.keyDown(document.body, { key: "d" });

		expect(presses()).toBe(0);
	});

	test("a plain inspector sheet does not silence it", async () => {
		const presses = await renderShortcut();
		const sheet = document.createElement("div");
		sheet.setAttribute("data-slot", "sheet-content");
		sheet.setAttribute("data-open", "");
		document.body.append(sheet);

		fireEvent.keyDown(document.body, { key: "d" });

		expect(presses()).toBe(1);
	});

	test("g then d is the tab chord, not the debug shortcut", async () => {
		const presses = await renderShortcut();
		fireEvent.keyDown(document.body, { key: "g" });
		fireEvent.keyDown(document.body, { key: "d" });
		expect(presses()).toBe(0);
	});

	test("an unrelated key after g ends the chord, so a later d opens the panel", async () => {
		const presses = await renderShortcut();
		fireEvent.keyDown(document.body, { key: "g" });
		fireEvent.keyDown(document.body, { key: "x" });
		fireEvent.keyDown(document.body, { key: "d" });
		expect(presses()).toBe(1);
	});

	test("the shortcut goes away with the component", async () => {
		let presses = 0;
		const { unmount } = await renderWithProviders(
			<Shortcut onPress={() => (presses += 1)} />,
		);
		unmount();
		fireEvent.keyDown(document.body, { key: "d" });
		expect(presses).toBe(0);
	});

	test("the latest callback runs, not the one from the first render", async () => {
		const seen: string[] = [];
		await renderWithProviders(<Changing seen={seen} />);
		fireEvent.click(screen.getByRole("button", { name: "change" }));

		fireEvent.keyDown(document.body, { key: "d" });

		expect(seen).toEqual(["second"]);
	});
});
