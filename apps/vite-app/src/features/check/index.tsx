// Public surface of the D2 check feature: the page the coordinator mounts
// at #/jobs/:id/check plus the explicit start handler C5 reuses. Reads stay
// inside CheckPage; this module commissions nothing.
export { CheckPage } from "@/features/check/CheckPage"
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
