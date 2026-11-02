import { describe, expect, mock, test } from "bun:test";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { ComposeEditor } from "./compose-editor";
import type { StackCheckState } from "./model";

const EMPTY: StackCheckState = {
	empty: true,
	pending: false,
	result: null,
	error: null,
};

function fileInput(container: HTMLElement): HTMLInputElement {
	const input = container.querySelector<HTMLInputElement>('input[type="file"]');
	if (!input) throw new Error("no file input");
	return input;
}

/** A controlled editor, the way the dialogs use it. */
function Controlled({
	initial = "",
	onChange,
}: {
	initial?: string;
	onChange?(next: string): void;
}) {
	const [value, setValue] = useState(initial);
	return (
		<ComposeEditor
			value={value}
			onChange={(next) => {
				setValue(next);
				onChange?.(next);
			}}
			state={EMPTY}
		/>
	);
}

describe("ComposeEditor", () => {
	test("labels the textarea and shows a Compose placeholder", () => {
		render(<ComposeEditor value="" onChange={() => {}} state={EMPTY} />);
		const textarea = screen.getByLabelText("docker-compose.yml");
		expect(textarea.tagName).toBe("TEXTAREA");
		expect(textarea).toHaveAttribute(
			"placeholder",
			expect.stringContaining("services:\n  web:\n    image: nginx:1.27"),
		);
	});

	test("a custom label and id are used for the field", () => {
		render(
			<ComposeEditor
				value=""
				onChange={() => {}}
				state={EMPTY}
				label="Compose"
				id="set-compose"
			/>,
		);
		expect(screen.getByLabelText("Compose")).toHaveAttribute(
			"id",
			"set-compose",
		);
	});

	test("typing reports the new text", async () => {
		const user = userEvent.setup();
		const onChange = mock((_next: string) => {});
		render(<Controlled onChange={onChange} />);
		await user.type(screen.getByLabelText("docker-compose.yml"), "ab");
		expect(onChange).toHaveBeenLastCalledWith("ab");
		expect(screen.getByLabelText("docker-compose.yml")).toHaveValue("ab");
	});

	test("draws the live check under the field", () => {
		render(<ComposeEditor value="" onChange={() => {}} state={EMPTY} />);
		expect(
			screen.getByText(/Paste or upload a Compose file/),
		).toBeInTheDocument();
	});

	test("the form's error is shown under the field and marks it invalid", () => {
		render(
			<ComposeEditor
				value="x"
				onChange={() => {}}
				state={EMPTY}
				error="A Compose file is required."
			/>,
		);
		expect(screen.getByText("A Compose file is required.")).toBeInTheDocument();
		expect(screen.getByLabelText("docker-compose.yml")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
	});

	test("a valid field is not marked invalid", () => {
		render(<ComposeEditor value="x" onChange={() => {}} state={EMPTY} />);
		expect(screen.getByLabelText("docker-compose.yml")).not.toHaveAttribute(
			"aria-invalid",
		);
	});

	test("flags a text over 256 KB, counting bytes not characters", () => {
		const { rerender } = render(
			<ComposeEditor
				value={"a".repeat(262144)}
				onChange={() => {}}
				state={EMPTY}
			/>,
		);
		expect(
			screen.queryByText("The Compose file is larger than 256 KB."),
		).not.toBeInTheDocument();
		// 131073 two-byte characters are 262146 bytes.
		rerender(
			<ComposeEditor
				value={"é".repeat(131073)}
				onChange={() => {}}
				state={EMPTY}
			/>,
		);
		expect(
			screen.getByText("The Compose file is larger than 256 KB."),
		).toBeInTheDocument();
		expect(screen.getByLabelText("docker-compose.yml")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
	});

	test("disabled turns off the textarea and the upload button", () => {
		render(
			<ComposeEditor value="" onChange={() => {}} state={EMPTY} disabled />,
		);
		expect(screen.getByLabelText("docker-compose.yml")).toBeDisabled();
		expect(screen.getByRole("button", { name: "Upload file" })).toBeDisabled();
	});

	test("the Upload button opens the file picker", async () => {
		const user = userEvent.setup();
		const { container } = render(
			<ComposeEditor value="" onChange={() => {}} state={EMPTY} />,
		);
		const clicked = mock(() => {});
		fileInput(container).addEventListener("click", clicked);
		await user.click(screen.getByRole("button", { name: "Upload file" }));
		expect(clicked).toHaveBeenCalledTimes(1);
	});

	test("an uploaded file replaces the text", async () => {
		const onChange = mock((_next: string) => {});
		const { container } = render(
			<Controlled initial="old" onChange={onChange} />,
		);
		const file = new File(["services:\n  web:\n    image: nginx\n"], "c.yml", {
			type: "text/yaml",
		});
		fireEvent.change(fileInput(container), { target: { files: [file] } });
		await waitFor(() =>
			expect(onChange).toHaveBeenCalledWith(
				"services:\n  web:\n    image: nginx\n",
			),
		);
		expect(screen.getByLabelText("docker-compose.yml")).toHaveValue(
			"services:\n  web:\n    image: nginx\n",
		);
	});

	test("a file over 256 KB is refused with its name, and the text stays", async () => {
		const onChange = mock((_next: string) => {});
		const { container } = render(
			<Controlled initial="keep" onChange={onChange} />,
		);
		const big = new File(["x"], "huge.yml");
		Object.defineProperty(big, "size", { value: 262145 });
		fireEvent.change(fileInput(container), { target: { files: [big] } });
		expect(
			await screen.findByText(
				"huge.yml is larger than 256 KB and was not added.",
			),
		).toBeInTheDocument();
		expect(onChange).not.toHaveBeenCalled();
		expect(screen.getByLabelText("docker-compose.yml")).toHaveValue("keep");
	});

	test("typing after a refused file clears the message", async () => {
		const user = userEvent.setup();
		const { container } = render(<Controlled />);
		const big = new File(["x"], "huge.yml");
		Object.defineProperty(big, "size", { value: 262145 });
		fireEvent.change(fileInput(container), { target: { files: [big] } });
		await screen.findByText(/huge.yml is larger/);
		await user.type(screen.getByLabelText("docker-compose.yml"), "a");
		expect(screen.queryByText(/huge.yml is larger/)).not.toBeInTheDocument();
	});

	test("choosing no file changes nothing", () => {
		const onChange = mock((_next: string) => {});
		const { container } = render(<Controlled onChange={onChange} />);
		fireEvent.change(fileInput(container), { target: { files: [] } });
		expect(onChange).not.toHaveBeenCalled();
	});
});
