package controlapi

import (
	"context"
	"github.com/lazyun/devbridge/internal/featurearea"
)

type featureAreaLifecycleService interface {
	Activate(context.Context, string, string, string) (featurearea.FeatureArea, error)
	Retire(context.Context, string, string, string) (featurearea.FeatureArea, error)
	Decompose(context.Context, featurearea.DecomposeInput) (featurearea.LifecycleResult, error)
}

func (f *Facade) ActivateFeatureArea(ctx context.Context, session, id, request string) (featurearea.FeatureArea, error) {
	service, ok := f.deps.FeatureAreas.(featureAreaLifecycleService)
	if !ok {
		return featurearea.FeatureArea{}, invalid("FeatureArea lifecycle unavailable")
	}
	value, err := service.Activate(ctx, session, id, request)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}
func (f *Facade) RetireFeatureArea(ctx context.Context, session, id, request string) (featurearea.FeatureArea, error) {
	service, ok := f.deps.FeatureAreas.(featureAreaLifecycleService)
	if !ok {
		return featurearea.FeatureArea{}, invalid("FeatureArea lifecycle unavailable")
	}
	value, err := service.Retire(ctx, session, id, request)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}
func (f *Facade) DecomposeFeatureArea(ctx context.Context, input featurearea.DecomposeInput) (featurearea.LifecycleResult, error) {
	service, ok := f.deps.FeatureAreas.(featureAreaLifecycleService)
	if !ok {
		return featurearea.LifecycleResult{}, invalid("FeatureArea lifecycle unavailable")
	}
	value, err := service.Decompose(ctx, input)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}
