import type { Variable } from "#/api/models";

/** A plain, runtime-scoped variable for tests; override what the test is about. */
export function makeVariable(overrides: Partial<Variable> = {}): Variable {
	return {
		key: "KEY",
		value: "value",
		isSecret: false,
		computedSecret: false,
		source: "manual",
		scope: "runtime",
		version: 1,
		comment: "",
		generated: [],
		references: [],
		rotateEveryDays: null,
		ageDays: 0,
		rotationDue: false,
		problem: undefined,
		updatedAt: "2026-01-01T00:00:00Z",
		...overrides,
	};
}
