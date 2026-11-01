import { VariablesTab as VariablesPanel } from "#/features/variables/variables-tab";
import type { ServiceTabProps } from "./types";

/** The Variables tab: table, RAW editor and platform variables. */
export function VariablesTab({ service }: ServiceTabProps) {
	return <VariablesPanel service={service} />;
}
