import { HugeiconsIcon } from "@hugeicons/react";
import {
  ArtificialIntelligence01Icon,
  FilterIcon,
  UserIcon,
} from "@hugeicons/core-free-icons";
import { Badge } from "@/components/ui/badge";
import { cn } from "cn";

export type ActorKind = "codex" | "jev" | "owner";

const ACTOR_COPY: Record<ActorKind, { label: string; hint: string }> = {
  codex: { label: "Codex", hint: "Work performed by Codex" },
  jev: {
    label: "Jev · classifier",
    hint: "Classification performed by Jev",
  },
  owner: { label: "Owner", hint: "Action or decision for the owner" },
};

const ACTOR_ICON = {
  codex: ArtificialIntelligence01Icon,
  jev: FilterIcon,
  owner: UserIcon,
} as const;

export function ActorLabel({
  actor,
  className,
}: {
  actor: ActorKind;
  className?: string;
}) {
  const copy = ACTOR_COPY[actor];
  return (
    <Badge
      variant="secondary"
      title={copy.hint}
      aria-label={copy.hint}
      className={cn("shrink-0", className)}
    >
      <HugeiconsIcon
        icon={ACTOR_ICON[actor]}
        strokeWidth={2}
        aria-hidden="true"
      />
      {copy.label}
    </Badge>
  );
}
