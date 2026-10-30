package controlapi

import (
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/reviewaccess"
)

func TestReviewInvalidArgumentUsesProductLanguage(t *testing.T) {
	mapped := MapError(review.ErrInvalidArgument)
	if mapped.Code != CodeInvalidArgument || mapped.Message != "invalid Review request" || !errors.Is(mapped, review.ErrInvalidArgument) {
		t.Fatalf("MapError(review.ErrInvalidArgument)=%+v", mapped)
	}
}

func TestReviewPolicyFailuresKeepStablePublicCodes(t *testing.T) {
	for _, test := range []struct {
		err  error
		code ErrorCode
	}{
		{&reviewaccess.Violation{Code: reviewaccess.FailureScopeForbidden, Operation: reviewaccess.OperationScope, Paths: []string{"../outside"}}, CodeReviewScopeForbidden},
	} {
		mapped := MapError(test.err)
		if mapped.Code != test.code || !errors.Is(mapped, test.err) {
			t.Fatalf("MapError(%v)=%+v", test.err, mapped)
		}
		if test.code == CodeReviewScopeForbidden && mapped.Message != "Feature Area Review access is outside the authorized Repository boundary: ../outside" {
			t.Fatalf("bounded offending paths missing: %+v", mapped)
		}
	}
}
