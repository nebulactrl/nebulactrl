import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { DitherToneBand } from "./tone-band";
import { TONE_BAND_TONES } from "./tone-band-math";

describe("DitherToneBand", () => {
	test("renders its children in a div that carries the tone and the band class", () => {
		render(<DitherToneBand tone="danger">Deploy failed</DitherToneBand>);
		const band = screen.getByText("Deploy failed");
		expect(band.tagName).toBe("DIV");
		expect(band).toHaveAttribute("data-slot", "dither-tone-band");
		expect(band).toHaveAttribute("data-tone", "danger");
		expect(band).toHaveClass("tone-band");
	});

	test("each tone is reflected in data-tone", () => {
		render(
			<>
				{TONE_BAND_TONES.map((tone) => (
					<DitherToneBand key={tone} tone={tone}>
						{`msg-${tone}`}
					</DitherToneBand>
				))}
			</>,
		);
		for (const tone of TONE_BAND_TONES) {
			expect(screen.getByText(`msg-${tone}`)).toHaveAttribute(
				"data-tone",
				tone,
			);
		}
	});

	test("merges className and forwards other props such as role", () => {
		render(
			<DitherToneBand tone="info" role="alert" className="p-2" id="note">
				Heads up
			</DitherToneBand>,
		);
		const band = screen.getByRole("alert");
		expect(band).toHaveClass("tone-band", "p-2");
		expect(band).toHaveAttribute("id", "note");
	});
});
