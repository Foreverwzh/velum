//go:build !darwin

package mobilebind

import "github.com/Foreverwzh/velum/core/pkg/netsense"

func netSnapshot(host Host) (netsense.Snapshot, error) { return snapshot(host.Network()) }
