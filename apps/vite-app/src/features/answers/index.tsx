// Public surface of the E3 answers feature: the page the coordinator mounts
// at #/jobs/:id/answers plus the explicit per-question save handler. Reads
// stay inside AnswersPage; this module commissions nothing.
export { AnswersPage } from "@/features/answers/AnswersPage"
export {
  buildAnswerValueSave,
  useAnswerSave,
} from "@/features/answers/useAnswerSave"
export type {
  AnswerBoxState,
  AnswerSaveSnapshot,
  UseAnswerSaveOptions,
  UseAnswerSaveResult,
} from "@/features/answers/useAnswerSave"
