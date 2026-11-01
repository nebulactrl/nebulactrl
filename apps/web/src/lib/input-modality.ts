// Whether the reader's last input was the keyboard. A drawer opened or
// closed from the keyboard skips its slide: the reader is moving through
// the page by key and the motion only makes them wait. Tracked from
// capture-phase listeners installed at import so the very first key press
// is already counted.

let keyboard = false;

if (typeof document !== "undefined") {
	document.addEventListener(
		"keydown",
		(event) => {
			// A held modifier is the start of a pointer gesture (ctrl-click),
			// not keyboard navigation.
			if (["Shift", "Control", "Alt", "Meta"].includes(event.key)) return;
			keyboard = true;
		},
		true,
	);
	for (const type of ["pointerdown", "mousedown", "touchstart"]) {
		document.addEventListener(
			type,
			() => {
				keyboard = false;
			},
			true,
		);
	}
}

export function lastInputWasKeyboard(): boolean {
	return keyboard;
}
