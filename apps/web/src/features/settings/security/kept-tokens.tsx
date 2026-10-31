import { Link } from "@tanstack/react-router";
import type { LiveTokenBody } from "#/api/gen/types.gen";
import { Relative } from "#/components/common/relative";

/**
 * What a new password did not end: the API tokens that still work. Changing or
 * resetting a password signs the other browsers out but keeps tokens (CI jobs
 * and agents use them), so the person sees them and can revoke any in
 * Account settings → API tokens. `revoked` is how many the person chose to
 * revoke along with the password.
 */
export function KeptTokens({
	tokens,
	revoked,
}: {
	tokens: readonly LiveTokenBody[];
	revoked: number;
}) {
	if (tokens.length === 0 && revoked === 0) return null;
	return (
		<div className="flex flex-col gap-2 text-pretty text-ink-2 text-xs leading-relaxed">
			{revoked > 0 ? (
				<p className="m-0">
					{revoked === 1
						? "1 API token was revoked."
						: `${revoked} API tokens were revoked.`}
				</p>
			) : null}
			{tokens.length > 0 ? (
				<>
					<p className="m-0">
						{tokens.length === 1
							? "This API token still works:"
							: `These ${tokens.length} API tokens still work:`}
					</p>
					<ul className="m-0 flex list-none flex-col gap-1 p-0">
						{tokens.map((token) => (
							<li
								key={token.id}
								className="flex min-w-0 flex-wrap items-baseline gap-x-2"
							>
								<span className="truncate font-medium text-ink-1">
									{token.name}
								</span>
								<span className="truncate font-mono text-2xs text-ink-3">
									{token.prefix}… · {token.teamName}
									{token.lastUsedAt ? (
										<>
											{" · last used "}
											<Relative iso={token.lastUsedAt} />
										</>
									) : (
										" · never used"
									)}
								</span>
							</li>
						))}
					</ul>
					<p className="m-0">
						Revoke any you do not recognise in{" "}
						<Link
							to="/account/$section"
							params={{ section: "tokens" }}
							className="text-accent-ink underline"
						>
							Account settings → API tokens
						</Link>
						.
					</p>
				</>
			) : null}
		</div>
	);
}
