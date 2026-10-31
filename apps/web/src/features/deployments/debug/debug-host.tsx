import { useState } from "react";
import { DeploymentDebugPanel } from "./debug-panel";
import { useDebugParam } from "./use-debug-param";

/**
 * Mounts the debug panel when the address says `&debug=<deploymentId>`, and
 * keeps showing that deployment while the panel slides out after the param is
 * cleared. Rendered once, by the canvas route, beside the inspector.
 */
export function DeploymentDebugHost() {
	const { deploymentId, close } = useDebugParam();
	const [held, setHeld] = useState<string | undefined>(deploymentId);
	if (deploymentId !== undefined && deploymentId !== held) {
		setHeld(deploymentId);
	}
	if (held === undefined) return null;
	return (
		<DeploymentDebugPanel
			key={held}
			deploymentId={held}
			open={deploymentId !== undefined}
			onClose={close}
		/>
	);
}
