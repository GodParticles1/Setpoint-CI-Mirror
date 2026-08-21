package integration

import "context"

type noopTaskProcessor struct{}

func (noopTaskProcessor) ProcessOne(context.Context) error { return nil }
