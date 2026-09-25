import { HugeiconsIcon } from "@hugeicons/react";
import {
  AlertCircleIcon,
  CheckmarkCircle01Icon,
} from "@hugeicons/core-free-icons";
import { cn } from "cn";

export const SEVEN_STAGES = [
  { id: "goals", label: "Your goals" },
  { id: "find", label: "Find jobs" },
  { id: "select", label: "Select jobs" },
  { id: "check", label: "Check job details" },
  { id: "answer", label: "Answer questions" },
  { id: "prepare", label: "Prepare applications" },
  { id: "review", label: "Review & send" },
] as const;

export type StageState = "complete" | "upcoming" | "blocked";

export type StageInput = {
  id: string;
  label: string;
  state: StageState;
};

export function StageProgress({
  stages,
  activeStageId = null,
  ariaLabel = "Application progress",
  className,
}: {
  stages?: StageInput[];
  activeStageId?: string | null;
  ariaLabel?: string;
  className?: string;
}) {
  const resolved: StageInput[] =
    stages ??
    SEVEN_STAGES.map((stage) => ({ ...stage, state: "upcoming" as const }));
  return (
    <ol
      aria-label={ariaLabel}
      className={cn("flex min-w-0 flex-wrap items-center gap-x-1 gap-y-2", className)}
    >
      {resolved.map((stage, index) => {
        const isActive =
          activeStageId !== null && stage.id === activeStageId;
        return (
          <li
            key={stage.id}
            aria-current={isActive ? "step" : undefined}
            className="flex min-w-0 items-center"
          >
            {index > 0 ? (
              <span
                aria-hidden="true"
                className="bg-border mx-1 h-px w-4 shrink-0 sm:w-6"
              />
            ) : null}
            <span
              className={cn(
                "inline-flex min-w-0 items-center gap-1.5 rounded-4xl border px-2.5 py-1 text-xs whitespace-nowrap sm:text-sm",
                isActive
                  ? "border-transparent bg-primary font-medium text-primary-foreground"
                  : stage.state === "complete"
                    ? "border-transparent bg-secondary text-secondary-foreground"
                    : stage.state === "blocked"
                      ? "border-destructive/40 text-destructive"
                      : "border-border text-muted-foreground",
              )}
            >
              {stage.state === "complete" && !isActive ? (
                <HugeiconsIcon
                  icon={CheckmarkCircle01Icon}
                  strokeWidth={2}
                  aria-hidden="true"
                  className="size-3.5 shrink-0"
                />
              ) : null}
              {stage.state === "blocked" && !isActive ? (
                <HugeiconsIcon
                  icon={AlertCircleIcon}
                  strokeWidth={2}
                  aria-hidden="true"
                  className="size-3.5 shrink-0"
                />
              ) : null}
              <span className="min-w-0 truncate">{stage.label}</span>
              <span className="sr-only">
                {isActive
                  ? " (current step)"
                  : stage.state === "complete"
                    ? " (completed)"
                    : stage.state === "blocked"
                      ? " (blocked)"
                      : " (not started)"}
              </span>
            </span>
          </li>
        );
      })}
    </ol>
  );
}
