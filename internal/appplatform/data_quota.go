package appplatform

// Per-installation data quota bounds (doc 185 section 3.1). The platform keeps one definition so the
// descriptor parser, the install review and the runner's admission agree.
const (
	DataQuotaMinBytes     int64 = 256 << 20
	DataQuotaMaxBytes     int64 = 16 << 30
	DataQuotaStepBytes    int64 = 64 << 20
	DataQuotaMinInodes    int64 = 65536
	DataQuotaMaxInodes    int64 = 1000000
	DataQuotaDefaultBytes       = DataQuotaMinBytes
	DataQuotaDefaultInode       = DataQuotaMinInodes
)

// ValidDataQuota reports whether the pair is inside the platform bounds.
func ValidDataQuota(bytes, inodes int64) bool {
	return bytes >= DataQuotaMinBytes && bytes <= DataQuotaMaxBytes && bytes%DataQuotaStepBytes == 0 &&
		inodes >= DataQuotaMinInodes && inodes <= DataQuotaMaxInodes
}

// QuotaOrDefault is the quota the descriptor requests: its block, or today's fixed limits.
func (d DataCompatibility) QuotaOrDefault() (bytes, inodes int64) {
	if d.Quota == nil {
		return DataQuotaDefaultBytes, DataQuotaDefaultInode
	}
	return int64(d.Quota.Bytes), int64(d.Quota.Inodes)
}
