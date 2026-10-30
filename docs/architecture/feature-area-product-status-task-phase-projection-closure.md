# FeatureArea product status and Task phase projection closure

Status: `FEATURE_AREA_PRODUCT_STATUS_AND_TASK_PHASE_PROJECTION_CLOSED`

FeatureArea lifecycle, current Task, latest Task, Task phase, and run activity now retain separate owners. FeatureArea product status comes only from the Core-derived `FeatureAreaProductProjection.domainStatus`; the internal lifecycle remains diagnostic. Workflow `currentTask` is the only source of the current Task identity; `latestTask` is projected independently for history and delivery facts. Task phase is read from that exact current Task.

The product FeatureArea table renders run activity only when the same FeatureArea projection has a current Task, a current operation with exact operation and Run identities, and an Agent operation state of `BUSY` or `RECOVERY_REQUIRED`. An idle `READY` conversation and its “Agent context is ready for work” message remain context readiness facts and are never presented as current run activity.

The empty-area result is therefore: the FeatureArea keeps its Core product status, while current Task, Task phase, and current run activity are empty. Historical Task and delivery facts remain available through the separate latest-Task projection.
