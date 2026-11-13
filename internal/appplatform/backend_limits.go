package appplatform

// The descriptor bounds on one backend's declared resources, exported for the
// runner's capacity envelope. They alias the generated contract constants so
// the two can never drift.
const (
	MinBackendMemoryMiB = minBackendMemoryMiB
	MaxBackendMemoryMiB = maxBackendMemoryMiB
	MinBackendCPUMillis = minBackendCPUMillis
	MaxBackendCPUMillis = maxBackendCPUMillis
)
