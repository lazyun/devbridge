package confirmation

import "github.com/lazyun/devbridge/internal/codexruntime"

// CanonicalizePublicCollections normalizes the collections the Purpose
// Confirmation Card requires to be present but that may legitimately hold zero
// items, so the public projection serializes them as [] rather than null.
//
// It runs only on a detached copy of an intent (never on durable bytes, never on
// the value returned by the store) and it must never fabricate an authoritative
// fact: a required field that is genuinely missing (content, projectId,
// contentHash, purpose body, variant identity) is left as-is so the Card still
// reports INTENT_FACTS_UNAVAILABLE / ENTRY_SCHEMA_INVALID. Only a nil slice that
// the entry contract says must always be an array is materialized as empty.
//
// The function changes none of: intentId, version, contentHash, durable bytes or
// hash, prepared generation, topology revision or development-context
// fingerprint. get and show share it so their envelopes stay in parity.
func CanonicalizePublicCollections(intent *Intent) {
	if intent == nil {
		return
	}
	// intent.steps is a required Card-entry array. A freshly prepared intent with
	// no committed steps carries an empty list, not null; the Card's
	// validateIntent rejects a non-array, so a nil here must become [].
	if intent.Steps == nil {
		intent.Steps = []Step{}
	}
	// content.variants is a required Card-entry array.
	if intent.Content.Variants == nil {
		intent.Content.Variants = []Variant{}
	}
}

// CanonicalizePublicOptions materializes the collections the Card validateOptions
// gate requires to always be present arrays that may legitimately hold zero
// items, so every public surface (MCP, HTTP, Console, Workbench, Desktop)
// serializes them as [] rather than JSON null. Like
// CanonicalizePublicCollections it runs only on a detached public copy and must
// never fabricate authority: it turns a nil Card-required list into an empty one
// and never invents a choice, eligibility, runtime or backend fact. It changes
// none of projectId, repositoryId, identity or contentHash.
func CanonicalizePublicOptions(options *Options) {
	if options == nil {
		return
	}
	if options.Projects == nil {
		options.Projects = []Choice{}
	}
	if options.Repositories == nil {
		options.Repositories = []Choice{}
	}
	if options.Branches == nil {
		options.Branches = []Choice{}
	}
	if options.Variants == nil {
		options.Variants = []Choice{}
	}
	if options.RuntimeOptions == nil {
		options.RuntimeOptions = []codexruntime.RuntimeOption{}
	}
	if options.DevelopmentContexts == nil {
		options.DevelopmentContexts = []DevelopmentContextOption{}
	}
	for index := range options.DevelopmentContexts {
		if options.DevelopmentContexts[index].Branches == nil {
			options.DevelopmentContexts[index].Branches = []Choice{}
		}
	}
}
