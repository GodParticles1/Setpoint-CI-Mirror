//go:build !linux

package xrocketreaddress

import (
	"errors"

	"setpoint/internal/executor"
)

func newProductionMutationAdapter(executor.CommandExecutor) (productionLocalAdapters, error) {
	return nil, errors.New("xRocket production mutation is supported only on Linux")
}
