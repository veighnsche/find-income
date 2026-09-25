// Public surface of the F4 attempts feature: per-role attempted-material
// history for Applications plus the immediate post-send outcome view the
// coordinator mounts at #/applications/:jobId/attempts/:reviewId. Reads
// stay inside these components; this module commissions nothing.
export { AttemptCard, attemptOutcomeText } from "@/features/attempts/AttemptCard"
export { AttemptHistory } from "@/features/attempts/AttemptHistory"
export {
  AttemptOutcomeView,
  attemptOutcomeHash,
} from "@/features/attempts/AttemptOutcome"
export {
  ATTEMPT_REVIEWS_KEY,
  MAX_ATTEMPT_REVIEWS_PER_ROLE,
  listAttemptReviews,
  recordAttemptReview,
  recordReviewAttempts,
} from "@/features/attempts/attempt-store"
