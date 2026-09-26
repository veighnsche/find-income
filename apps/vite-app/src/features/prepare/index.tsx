// Public surface of the D5 prepare feature: the page the coordinator mounts
// at #/jobs/:id/prepare plus the explicit prepare/edit/rewrite actions.
// Reads stay inside PreparePage; this module commissions nothing.
export { PreparePage } from "@/features/prepare/PreparePage"
export {
  MATERIAL_EDIT_BYTE_LIMIT,
  REWRITE_INSTRUCTION_RUNE_LIMIT,
  buildMaterialEditRequest,
  buildMaterialPrepareRequest,
  buildMaterialRewriteRequest,
  countBytes,
  countRunes,
  newPrepareRequestKey,
  useMaterialEdit,
  useMaterialRewrite,
  usePrepareStart,
} from "@/features/prepare/usePrepareActions"
export type {
  UseMaterialEditOptions,
  UseMaterialEditResult,
  UseMaterialRewriteOptions,
  UseMaterialRewriteResult,
  UsePrepareStartOptions,
  UsePrepareStartResult,
} from "@/features/prepare/usePrepareActions"
