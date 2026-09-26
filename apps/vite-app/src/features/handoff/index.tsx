// Public surface of the H handoff feature: the page the coordinator mounts
// at #/jobs/:id/handoff. Reads stay inside HandoffPage; this module
// commissions nothing and the page never contacts employers.
export { HandoffPage } from "@/features/handoff/HandoffPage"
