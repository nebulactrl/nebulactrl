import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "cn";
import { useMemo } from "react";

import { ToneIcon } from "#/components/common/tone-icon";
import { Label } from "#/components/ui/label";

const fieldVariants = cva("group/field flex w-full gap-2", {
	variants: {
		orientation: {
			// A child with its own arbitrary width (slug 240, name 360) keeps it:
			// controls are sized to their content, never stretched.
			vertical: "flex-col [&>*:not([class*='w-['])]:w-full [&>.sr-only]:w-auto",
			horizontal:
				"flex-row items-center has-[>[data-slot=field-content]]:items-start *:data-[slot=field-label]:flex-auto has-[>[data-slot=field-content]]:[&>[role=checkbox],[role=radio]]:mt-px",
			responsive:
				"flex-col *:w-full @md/field-group:flex-row @md/field-group:items-center @md/field-group:*:w-auto @md/field-group:has-[>[data-slot=field-content]]:items-start @md/field-group:*:data-[slot=field-label]:flex-auto [&>.sr-only]:w-auto @md/field-group:has-[>[data-slot=field-content]]:[&>[role=checkbox],[role=radio]]:mt-px",
		},
	},
	defaultVariants: {
		orientation: "vertical",
	},
});

function Field({
	className,
	orientation = "vertical",
	...props
}: React.ComponentProps<"div"> & VariantProps<typeof fieldVariants>) {
	return (
		<div
			role="group"
			data-slot="field"
			data-orientation={orientation}
			className={cn(fieldVariants({ orientation }), className)}
			{...props}
		/>
	);
}

function FieldLabel({
	className,
	...props
}: React.ComponentProps<typeof Label>) {
	return (
		<Label
			data-slot="field-label"
			className={cn(
				"group/field-label peer/field-label flex w-fit gap-2 group-data-[disabled=true]/field:opacity-50 has-[>[data-slot=field]]:cursor-pointer has-[>[data-slot=field]]:rounded-card has-[>[data-slot=field]]:border has-[>[data-slot=field]]:border-line-strong has-[>[data-slot=field]]:transition-[border-color,background-color] has-[>[data-slot=field]]:duration-quick has-[>[data-slot=field]]:ease-out has-[>[data-slot=field]]:not-has-[:disabled,[data-disabled]]:hover:border-line-control has-[>[data-slot=field]]:has-data-checked:border-accent-ink has-[>[data-slot=field]]:has-data-checked:bg-accent-wash has-[>[data-slot=field]]:has-[:focus-visible]:focus-ring *:data-[slot=field]:px-3 *:data-[slot=field]:py-2.5",
				"has-[>[data-slot=field]]:w-full has-[>[data-slot=field]]:flex-col",
				className,
			)}
			{...props}
		/>
	);
}

function FieldTitle({ className, ...props }: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="field-label"
			className={cn(
				"type-panel-title flex w-fit items-center gap-2 group-data-[disabled=true]/field:opacity-50",
				className,
			)}
			{...props}
		/>
	);
}

function FieldDescription({ className, ...props }: React.ComponentProps<"p">) {
	return (
		<p
			data-slot="field-description"
			className={cn(
				"type-secondary text-left group-has-data-horizontal/field:text-balance [[data-variant=legend]+&]:-mt-1.5",
				"last:mt-0 nth-last-2:-mt-1",
				className,
			)}
			{...props}
		/>
	);
}

function FieldError({
	className,
	children,
	errors,
	...props
}: React.ComponentProps<"div"> & {
	errors?: Array<{ message?: string } | undefined>;
}) {
	const content = useMemo(() => {
		if (children) {
			return children;
		}

		if (!errors?.length) {
			return null;
		}

		const uniqueErrors = [
			...new Map(errors.map((error) => [error?.message, error])).values(),
		];

		if (uniqueErrors?.length === 1) {
			return uniqueErrors[0]?.message;
		}

		return (
			<ul className="ml-4 flex list-disc flex-col gap-1">
				{uniqueErrors.map(
					(error, index) =>
						error?.message && <li key={index}>{error.message}</li>,
				)}
			</ul>
		);
	}, [children, errors]);

	if (!content) {
		return null;
	}

	return (
		<div
			role="alert"
			data-slot="field-error"
			data-tone="danger"
			className={cn(
				"tone-callout relative flex items-start gap-2 rounded-ctl px-2.5 py-1.5 text-status-failed-on text-xs leading-[1.45]",
				className,
			)}
			{...props}
		>
			<ToneIcon tone="danger" className="mt-px" />
			<div className="min-w-0">{content}</div>
		</div>
	);
}

export { Field, FieldDescription, FieldError, FieldLabel, FieldTitle };
