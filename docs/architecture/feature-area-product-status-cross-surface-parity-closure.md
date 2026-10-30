# FeatureArea product status cross-surface parity closure

Status: `FEATURE_AREA_PRODUCT_STATUS_CROSS_SURFACE_PARITY_CLOSED`

The canonical read contract is `workflowpresentation.FeatureAreaProductProjection`. It is derived in Core and contains:

- `domainStatus`
- `currentTask`
- `latestTask`
- `taskPhase`
- `operationActivity`
- `requiredAction`
- `availableActions`
- `technicalDetails`

`FeatureAreaDomainStatus` is the only lifecycle-to-product mapper. `DISCOVERING`, `ACTIVE`, and `RETIRING` map to `ACTIVE`; `RETIRED`, `DECOMPOSED`, and legacy read-only records map to `CLOSED`. Unknown internal values produce no `domainStatus` and retain `UNKNOWN_FEATURE_AREA_INTERNAL_STATUS` in `technicalDetails`. The internal lifecycle remains available as diagnostic compatibility data and is not a product filter or display source.

Current workflow facts retain their distinct authorities. `currentTask` comes only from workflow guidance. `latestTask` is historical. `taskPhase` is copied only from `currentTask`. `operationActivity` is admitted only when a current Task and complete operation and Run identities exist and the Agent state is `BUSY` or `RECOVERY_REQUIRED`. READY context text is excluded. `requiredAction` is the required workflow interaction; ordinary capabilities such as `CREATE_TASK` are listed under `availableActions`.

## Surface contract

| Surface | Canonical machine path |
| --- | --- |
| Core workspace inventory | `featureAreas[].product` |
| Core selected FeatureArea | `presentation.product` |
| MCP `workspace_get` | `result.featureAreaProduct` |
| MCP `work_list` FeatureArea page | `result.featureAreas[].product` |
| MCP `operation_get` | `result.featureAreaProduct` |
| HTTP / Console project workspace | `featureAreas[].product` |
| WebUI Product Workbench | `featureAreas[].product` |
| Desktop embedded workbench | same Product Workbench payload and JavaScript |
| ChatGPT Card | inventory or selected projection `product` |
| JSON and generated schemas | the same DTO fields inferred from the Go contract |

WebUI and Card validate the two allowed values and never translate the internal five-state lifecycle. Active counts and ACTIVE/CLOSED filters use `domainStatus`. Unknown values remain visible only in unfiltered diagnostic contexts and match neither product filter.

Focused acceptance covers Core mapping, unknown diagnostics, current/latest Task ownership, Task phase, strict operation activity admission, HTTP/Console JSON, MCP generated schemas, WebUI counts/filters/tables, the Desktop-shared Product Workbench bundle, and Card rendering. The closure adds no database state or migration.
