// Public surface of the prepare feature: the page the coordinator mounts
// at #/jobs/:id/prepare plus the explicit request helpers. Reads stay
// inside the page components; this module commissions nothing.
export { PreparePage } from "@/features/prepare/PreparePage"
export {
  artifactStateLabel,
  artifactTypeLabel,
  copyText,
  downloadBlob,
  downloadText,
} from "@/features/prepare/ArtifactsSection"
export type {
  ArtifactType,
  EffectiveArtifactState,
} from "@/features/prepare/ArtifactsSection"
export {
  buildClarificationAnswerRequest,
  buildHandoffSaveRequest,
  buildMaterialPrepareRequest,
  buildMaterialRewriteRequest,
  countBytes,
  countRunes,
  newPrepareRequestKey,
} from "@/features/prepare/artifactsApi"
export type { StoredArtifactType } from "@/features/prepare/artifactsApi"
