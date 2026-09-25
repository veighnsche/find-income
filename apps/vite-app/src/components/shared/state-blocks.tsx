import { HugeiconsIcon } from "@hugeicons/react";
import {
  AlertCircleIcon,
  InformationCircleIcon,
  PauseCircleIcon,
} from "@hugeicons/core-free-icons";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "cn";

export function LoadingBlock({
  label,
  detail,
  className,
}: {
  label: string;
  detail?: string;
  className?: string;
}) {
  return (
    <div
      role="status"
      aria-label={label}
      className={cn("flex min-w-0 flex-col gap-3", className)}
    >
      <Skeleton className="h-4 w-3/4" aria-hidden="true" />
      <Skeleton className="h-4 w-1/2" aria-hidden="true" />
      <p className="text-sm wrap-break-word text-muted-foreground">{label}</p>
      {detail !== undefined ? (
        <p className="text-sm wrap-break-word text-muted-foreground">{detail}</p>
      ) : null}
    </div>
  );
}

export function ErrorBlock({
  title = "Something went wrong",
  message,
  onRetry,
  retryLabel = "Try again",
  className,
}: {
  title?: string;
  message: string;
  onRetry?: () => void;
  retryLabel?: string;
  className?: string;
}) {
  return (
    <Alert variant="destructive" className={className}>
      <HugeiconsIcon icon={AlertCircleIcon} strokeWidth={2} aria-hidden="true" />
      <AlertTitle className="wrap-break-word">{title}</AlertTitle>
      <AlertDescription className="wrap-break-word">{message}</AlertDescription>
      {onRetry !== undefined ? (
        <div className="col-start-2 mt-2">
          <Button variant="outline" size="sm" onClick={onRetry}>
            {retryLabel}
          </Button>
        </div>
      ) : null}
    </Alert>
  );
}

export function EmptyBlock({
  title,
  description,
  actionLabel,
  onAction,
  className,
}: {
  title: string;
  description?: string;
  actionLabel?: string;
  onAction?: () => void;
  className?: string;
}) {
  const showAction = actionLabel !== undefined && onAction !== undefined;
  return (
    <Empty className={className}>
      <EmptyHeader>
        <EmptyTitle className="wrap-break-word">{title}</EmptyTitle>
        {description !== undefined ? (
          <EmptyDescription className="wrap-break-word">
            {description}
          </EmptyDescription>
        ) : null}
      </EmptyHeader>
      {showAction ? (
        <EmptyContent>
          <Button variant="outline" size="sm" onClick={onAction}>
            {actionLabel}
          </Button>
        </EmptyContent>
      ) : null}
    </Empty>
  );
}

export function PausedBlock({
  title = "Paused",
  message,
  onResume,
  resumeLabel = "Resume",
  className,
}: {
  title?: string;
  message: string;
  onResume?: () => void;
  resumeLabel?: string;
  className?: string;
}) {
  return (
    <Empty className={className}>
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <HugeiconsIcon icon={PauseCircleIcon} strokeWidth={2} aria-hidden="true" />
        </EmptyMedia>
        <EmptyTitle className="wrap-break-word">{title}</EmptyTitle>
        <EmptyDescription className="wrap-break-word">
          {message}
        </EmptyDescription>
      </EmptyHeader>
      {onResume !== undefined ? (
        <EmptyContent>
          <Button variant="outline" size="sm" onClick={onResume}>
            {resumeLabel}
          </Button>
        </EmptyContent>
      ) : null}
    </Empty>
  );
}

export function UnsupportedBlock({
  title = "Not available yet",
  message,
  className,
}: {
  title?: string;
  message: string;
  className?: string;
}) {
  return (
    <Empty className={className}>
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <HugeiconsIcon
            icon={InformationCircleIcon}
            strokeWidth={2}
            aria-hidden="true"
          />
        </EmptyMedia>
        <EmptyTitle className="wrap-break-word">{title}</EmptyTitle>
        <EmptyDescription className="wrap-break-word">
          {message}
        </EmptyDescription>
      </EmptyHeader>
    </Empty>
  );
}
