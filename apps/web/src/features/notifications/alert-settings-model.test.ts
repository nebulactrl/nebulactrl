import { describe, expect, test } from "bun:test";
import {
	ALERT_FIELDS,
	type AlertSettings,
	isDirty,
	parseForm,
	toForm,
} from "./alert-settings-model";

const defaults: AlertSettings = {
	crashGraceSeconds: 30,
	unhealthyGraceSeconds: 120,
	loopRestarts: 3,
	loopWindowMinutes: 10,
	recoverAfterSeconds: 120,
	flapIncidents: 3,
	flapWindowMinutes: 60,
	backupGraceMinutes: 0,
	logLines: 5,
};

describe("alert settings form", () => {
	test("a form round-trips the settings", () => {
		const parsed = parseForm(toForm(defaults));
		expect(parsed.errors).toEqual({});
		expect(parsed.value).toEqual(defaults);
	});

	test("every field has a hint and sane limits", () => {
		for (const f of ALERT_FIELDS) {
			expect(f.hint.length).toBeGreaterThan(10);
			expect(f.min).toBeLessThan(f.max);
		}
		const keys: string[] = ALERT_FIELDS.map((f) => f.key);
		expect(keys.sort()).toEqual(Object.keys(defaults).sort());
	});

	test("out-of-range and non-numeric input is reported per field", () => {
		const form = toForm(defaults);
		form.loopRestarts = "1";
		form.logLines = "99";
		form.crashGraceSeconds = "abc";
		form.flapWindowMinutes = "1.5";
		const { value, errors } = parseForm(form);
		expect(value).toBeNull();
		expect(errors.loopRestarts).toBe("Between 2 and 50.");
		expect(errors.logLines).toBe("Between 0 and 20.");
		expect(errors.crashGraceSeconds).toBe("Enter a whole number.");
		expect(errors.flapWindowMinutes).toBe("Enter a whole number.");
		expect(errors.unhealthyGraceSeconds).toBeUndefined();
	});

	test("zero is valid where the server allows it", () => {
		const form = toForm(defaults);
		form.crashGraceSeconds = "0";
		form.backupGraceMinutes = "0";
		expect(parseForm(form).value?.crashGraceSeconds).toBe(0);
	});

	test("dirty means different from what is saved", () => {
		const form = toForm(defaults);
		expect(isDirty(defaults, form)).toBe(false);
		form.logLines = " 5 ";
		expect(isDirty(defaults, form)).toBe(false);
		form.logLines = "6";
		expect(isDirty(defaults, form)).toBe(true);
	});
});
