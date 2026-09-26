export { ActorLabel } from "@/components/shared/actor-label";
export type { ActorKind } from "@/components/shared/actor-label";
export { ActivityDisclosure } from "@/components/shared/activity-disclosure";
export type {
  ActivityEntry,
  ActivityEntryKind,
} from "@/components/shared/activity-disclosure";
export { ShellNav } from "@/components/shared/shell-nav";
export type { ShellNavItem } from "@/components/shared/shell-nav";
export { SEVEN_STAGES, StageProgress } from "@/components/shared/stage-progress";
export type {
  StageInput,
  StageState,
} from "@/components/shared/stage-progress";
export {
  EmptyBlock,
  ErrorBlock,
  LoadingBlock,
  PausedBlock,
  UnsupportedBlock,
} from "@/components/shared/state-blocks";
export {
  notifyAccepted,
  scopeVersion,
  useInvalidate,
  useScopeEpoch,
} from "@/components/shared/invalidation";
export type { InvalidationScope } from "@/components/shared/invalidation";
export {
  SavedGoalsProvider,
  notifyGoalsAccepted,
  useSavedGoals,
} from "@/components/shared/saved-goals";
export type { SavedGoalsValue } from "@/components/shared/saved-goals";
export {
  RUN_NOT_FOUND,
  RunNotFoundError,
  isRunNotFound,
  restoreRunFromServer,
  useServerRun,
} from "@/components/shared/run-restore";
export type { ServerRunResult } from "@/components/shared/run-restore";
export {
  hasGoalEditorOpener,
  registerGoalEditorOpener,
  requestGoalEditorOpen,
  useRegisterGoalEditorOpener,
} from "@/components/shared/goal-editor";
export type { GoalEditorOpener } from "@/components/shared/goal-editor";
export {
  clearAnswerDraft,
  clearAnswerDraftsForCheck,
  clearAnswerDraftsForJob,
  getAnswerDraft,
  hasAnswerDraft,
  setAnswerDraft,
  useAnswerDraft,
} from "@/components/shared/answer-drafts";
export type {
  AnswerDraftHandle,
  AnswerDraftKey,
} from "@/components/shared/answer-drafts";
