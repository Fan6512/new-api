package model

// UnaChannelMirror 保存 Business 权威映射在 NewAPI 中的应用版本，
// 只记录同步元数据，不保存上游密钥、请求内容或用户数据。
type UnaChannelMirror struct {
	ID                 int    `gorm:"primaryKey"`
	MappingID          string `gorm:"type:varchar(64);uniqueIndex;not null"`
	ChannelID          int    `gorm:"index;not null"`
	Model              string `gorm:"type:varchar(100);not null"`
	ChannelName        string `gorm:"type:varchar(100);not null"`
	UpstreamModel      string `gorm:"type:varchar(150);not null"`
	Priority           int64  `gorm:"not null"`
	Weight             int64  `gorm:"not null"`
	Enabled            bool   `gorm:"not null"`
	DesiredVersion     uint64 `gorm:"not null"`
	RequestFingerprint string `gorm:"type:char(64);not null"`
	CreatedTime        int64  `gorm:"bigint;not null"`
	UpdatedTime        int64  `gorm:"bigint;not null"`
}
