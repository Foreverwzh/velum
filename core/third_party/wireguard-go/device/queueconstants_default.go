//go:build !android && !ios && !windows

/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2017-2025 WireGuard LLC. All Rights Reserved.
 */

package device

import "github.com/sagernet/wireguard-go/conn"

const (
	QueueStagedSize            = conn.IdealBatchSize
	QueueOutboundSize          = 1024
	QueueInboundSize           = 1024
	QueueHandshakeSize         = 1024
	MaxSegmentSize             = 2048 - 32 // Velum：上游是 64KB，见 VELUM.md
	PreallocatedBuffersPerPool = 0             // Disable and allow for infinite memory growth
)
