import { HugeiconsIcon } from "@hugeicons/react";
import {
  Activity01Icon,
  AlertCircleIcon,
  ArrowRight01Icon,
  ChevronDownIcon,
  InformationCircleIcon,
} from "@hugeicons/core-free-icons";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { ActorLabel, type ActorKind } from "@/components/shared/actor-label";
import { cn } from "cn";

export type ActivityEntryKind = "action" | "result" | "source" | "blocker";

export type ActivityEntry = {
  id: string;
  kind: ActivityEntryKind;
  text: string;
  href?: string;
};

const ENTRY_ICON = {
  action: Activity01Icon,
  result: InformationCircleIcon,
  source: ArrowRight01Icon,
  blocker: AlertCircleIcon,
} as const;

const ENTRY_KIND_LABEL: Record<ActivityEntryKind, string> = {
  action: "Action",
  result: "Result",
  source: "Source",
  blocker: "Blocker",
};

export function ActivityDisclosure({
  actor,
  phase,
  status,
  entries,
  defaultOpen = false,
  open,
  onOpenChange,
  emptyText = "No activity recorded yet.",
  className,
}: {
  actor: ActorKind;
  phase: string;
  status: string;
  entries: ActivityEntry[];
  defaultOpen?: boolean;
  open?: boolean;
  onOpenChange?: (nextOpen: boolean) => void;
  emptyText?: string;
  className?: string;
}) {
  const entryCount = entries.length;
  return (
    <Collapsible
      defaultOpen={defaultOpen}
      open={open}
      onOpenChange={
        onOpenChange === undefined
          ? undefined
          : (nextOpen) => {
              onOpenChange(nextOpen);
            }
      }
      className={cn("min-w-0 rounded-lg border", className)}
    >
      <CollapsibleTrigger className="group/activity-trigger flex w-full min-w-0 cursor-pointer items-center gap-2 rounded-lg px-3 py-2.5 text-left outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50">
        <ActorLabel actor={actor} />
        <span className="min-w-0 flex-1 wrap-break-word text-sm font-medium">
          {phase}
        </span>
        <span className="hidden min-w-0 shrink-0 wrap-break-word text-xs text-muted-foreground sm:inline">
          {status}
        </span>
        <span className="shrink-0 text-xs text-muted-foreground tabular-nums">
          {entryCount} {entryCount === 1 ? "entry" : "entries"}
        </span>
        <HugeiconsIcon
          icon={ChevronDownIcon}
          strokeWidth={2}
          aria-hidden="true"
          className="size-4 shrink-0 transition-transform group-data-[panel-open]/activity-trigger:rotate-180"
        />
      </CollapsibleTrigger>
      <CollapsibleContent className="border-t px-3 py-2 data-closed:hidden">
        <p className="py-1 text-xs wrap-break-word text-muted-foreground sm:hidden">
          {status}
        </p>
        {entryCount === 0 ? (
          <p className="py-2 text-sm wrap-break-word text-muted-foreground">
            {emptyText}
          </p>
        ) : (
          <div className="max-h-64 overflow-y-auto py-1">
            <ul className="flex min-w-0 flex-col gap-1.5">
              {entries.map((entry) => (
                <li
                  key={entry.id}
                  className={cn(
                    "flex min-w-0 items-start gap-2 text-sm",
                    entry.kind === "blocker" && "text-destructive",
                  )}
                >
                  <HugeiconsIcon
                    icon={ENTRY_ICON[entry.kind]}
                    strokeWidth={2}
                    aria-hidden="true"
                    className="mt-0.5 size-4 shrink-0"
                  />
                  <span className="min-w-0 flex-1 wrap-break-word">
                    <span className="sr-only">
                      {ENTRY_KIND_LABEL[entry.kind]}:{" "}
                    </span>
                    {entry.href !== undefined ? (
                      <a
                        href={entry.href}
                        className="underline underline-offset-4 outline-none hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50"
                      >
                        {entry.text}
                      </a>
                    ) : (
                      entry.text
                    )}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}
      </CollapsibleContent>
    </Collapsible>
  );
}
