package task

// ReviewDepth is the semantic depth of a Review, independent of scope, model,
// reasoning effort, permissions, and the authored instruction.
type ReviewDepth string

const (
	ReviewDepthFast ReviewDepth = "FAST"
	ReviewDepthDeep ReviewDepth = "DEEP"
)

func (d ReviewDepth) Valid() bool { return d == ReviewDepthFast || d == ReviewDepthDeep }

func (d ReviewDepth) Satisfies(required ReviewDepth) bool {
	return d.Valid() && required.Valid() && (d == ReviewDepthDeep || required == ReviewDepthFast)
}

func ReviewDepthPointer(d ReviewDepth) *ReviewDepth { return &d }

// AllowedReviewDepths projects the durable floor without inferring progression.
func AllowedReviewDepths(required *ReviewDepth) []ReviewDepth {
	if required == nil {
		return []ReviewDepth{}
	}
	switch *required {
	case ReviewDepthFast:
		return []ReviewDepth{ReviewDepthFast, ReviewDepthDeep}
	case ReviewDepthDeep:
		return []ReviewDepth{ReviewDepthDeep}
	default:
		return []ReviewDepth{}
	}
}
