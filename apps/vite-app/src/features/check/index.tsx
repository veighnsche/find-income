// Public surface of the D2/C1 check feature: the page the coordinator
// mounts at #/jobs/:id/check, the explicit start handler discovery's bulk
// check reuses, and the explicit zero-question continuation. Reads stay
// inside CheckPage; this module commissions nothing.
export { CheckPage } from "@/features/check/CheckPage"
export { ZeroQuestionContinuation } from "@/features/check/zero-question-continuation"
export {
  CheckActivityFeed,
  checkEntryKindFor,
  isCheckBlockerEvent,
  toCheckActivityEntry,
} from "@/features/check/check-activity-feed"
export type { CheckActivityFeedProps } from "@/features/check/check-activity-feed"
export {
  buildCheckStartRequest,
  newCheckRequestKey,
  useCheckStart,
} from "@/features/check/useCheckStart"
export type {
  CheckStartRevisions,
  UseCheckStartOptions,
  UseCheckStartResult,
} from "@/features/check/useCheckStart"
