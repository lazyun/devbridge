package controlapi

import (
	"context"
	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/featurearea"
)

type featureAreaPlanStore interface {
	PrepareFeatureAreaDeliveryPlan(context.Context, string, string) (delivery.FeatureAreaPlan, error)
	CommitFeatureAreaPlan(context.Context, string, string, string, delivery.FeatureAreaPlan) (featurearea.FeatureArea, error)
}
