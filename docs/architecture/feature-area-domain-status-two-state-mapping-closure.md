# FeatureArea product status two-state mapping closure

The Core `FeatureArea.status` lifecycle remains the authoritative five-state
contract: `DISCOVERING`, `ACTIVE`, `RETIRING`, `RETIRED`, and `DECOMPOSED`.
Product surfaces do not expose those lifecycle transitions as current work
status.

The canonical product mapping is:

| Core lifecycle | Product status |
| --- | --- |
| `DISCOVERING`, `ACTIVE`, `RETIRING` | `ACTIVE` |
| `RETIRED`, `DECOMPOSED` | `CLOSED` |
| any legacy read-only FeatureArea | `CLOSED` |

Unknown lifecycle values map to no product status. They remain visibly
unavailable in an unfiltered diagnostic/all view and do not enter either the
active or closed filter.

`workflowpresentation.FeatureAreaDomainStatus` owns this mapping. It is used by
the Core read-side `FeatureAreaProductProjection`; HTTP, MCP, Console, WebUI,
Desktop, and ChatGPT Card consume the resulting `product.domainStatus` machine
field. Clients only validate `ACTIVE` or `CLOSED`; they do not translate the
five-state lifecycle. Task phase and current run activity remain fields of the
same Core projection and retain their separate authoritative inputs.
