import { Segmented } from "#/components/ui/segmented";
import { ROLE_OPTIONS, roleConsequence, type ServerRole } from "./model";

/**
 * A server's role: Deployments, Builds or Both. The sentence under the control
 * says what the choice means for placement and for builds, so the consequence
 * is read before Save, not discovered after.
 */
export function RoleControl({
	value,
	onChange,
}: {
	value: ServerRole;
	onChange: (role: ServerRole) => void;
}) {
	return (
		<div className="flex flex-col gap-2">
			<Segmented
				value={value}
				onValueChange={onChange}
				options={ROLE_OPTIONS.map(({ value: role, label }) => ({
					value: role,
					label,
				}))}
				aria-label="Server role"
				font="sans"
				density="regular"
				className="self-start"
			/>
			<p className="type-secondary m-0" role="status">
				{roleConsequence(value)}
			</p>
		</div>
	);
}
