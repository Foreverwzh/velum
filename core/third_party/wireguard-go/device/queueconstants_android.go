/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2017-2025 WireGuard LLC. All Rights Reserved.
 */

package device

import "github.com/sagernet/wireguard-go/conn"

/* Reduce memory consumption for Android */

const (
	QueueStagedSize            = conn.IdealBatchSize
	QueueOutboundSize          = 1024
	QueueInboundSize           = 1024
	QueueHandshakeSize         = 1024
	MaxSegmentSize             = 2048 - 32 // Velum：上游是 64KB，见 VELUM.md
	PreallocatedBuffersPerPool = 4096
)
