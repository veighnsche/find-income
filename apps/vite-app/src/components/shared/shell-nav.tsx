import { Badge } from "@/components/ui/badge";
import { cn } from "cn";

export type ShellNavItem = {
  id: string;
  label: string;
  href?: string;
  active?: boolean;
  disabled?: boolean;
  badge?: string;
};

export function ShellNav({
  items,
  ariaLabel,
  className,
}: {
  items: ShellNavItem[];
  ariaLabel: string;
  className?: string;
}) {
  return (
    <nav aria-label={ariaLabel} className={cn("min-w-0", className)}>
      <ul className="flex min-w-0 flex-wrap items-center gap-1">
        {items.map((item) => (
          <li key={item.id} className="flex min-w-0 items-center">
            <ShellNavEntry item={item} />
          </li>
        ))}
      </ul>
    </nav>
  );
}

function ShellNavEntry({ item }: { item: ShellNavItem }) {
  const content = (
    <>
      <span className="min-w-0 wrap-break-word">{item.label}</span>
      {item.badge ? (
        <Badge variant="secondary" className="ml-1.5 shrink-0">
          {item.badge}
        </Badge>
      ) : null}
    </>
  );

  const classes =
    "inline-flex min-w-0 max-w-full items-center rounded-4xl px-3 py-1.5 text-sm transition-colors outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50";

  if (item.disabled === true) {
    return (
      <span
        aria-disabled="true"
        title={item.label}
        className={cn(classes, "cursor-not-allowed opacity-50")}
      >
        {content}
      </span>
    );
  }

  if (item.active === true) {
    if (item.href !== undefined) {
      return (
        <a
          href={item.href}
          aria-current="page"
          title={item.label}
          className={cn(classes, "bg-secondary font-medium text-secondary-foreground")}
        >
          {content}
        </a>
      );
    }
    return (
      <span aria-current="page" title={item.label} className={cn(classes, "bg-secondary font-medium text-secondary-foreground")}>
        {content}
      </span>
    );
  }

  if (item.href === undefined) {
    return (
      <span title={item.label} className={cn(classes, "text-muted-foreground")}>
        {content}
      </span>
    );
  }

  return (
    <a
      href={item.href}
      title={item.label}
      className={cn(classes, "text-muted-foreground hover:bg-muted hover:text-foreground")}
    >
      {content}
    </a>
  );
}
