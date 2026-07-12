package model

import (
	"context"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type UnaTokenProvision struct {
	Id                 int64  `json:"id" gorm:"primaryKey"`
	IdempotencyKey     string `json:"-" gorm:"type:varchar(128);uniqueIndex;not null"`
	RequestFingerprint string `json:"-" gorm:"type:char(64);not null"`
	TokenId            int    `json:"token_id" gorm:"index;not null"`
	EncryptedKeyBody   []byte `json:"-" gorm:"type:blob;not null"`
	Nonce              []byte `json:"-" gorm:"type:varbinary(12);not null"`
	KekVersion         string `json:"-" gorm:"type:varchar(32);not null"`
	RecoverableUntil   int64  `json:"-" gorm:"bigint;index;not null"`
	CreatedTime        int64  `json:"created_time" gorm:"bigint;not null"`
	UpdatedTime        int64  `json:"updated_time" gorm:"bigint;not null"`
}

func (UnaTokenProvision) TableName() string { return "una_token_provisions" }

func DisableUnaTokenByID(ctx context.Context, id int) (*Token, error) {
	var token Token
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&token, "id = ?", id).Error; err != nil {
			return err
		}
		if token.Status != common.TokenStatusDisabled {
			if err := tx.Model(&Token{}).Where("id = ?", id).Update("status", common.TokenStatusDisabled).Error; err != nil {
				return err
			}
			token.Status = common.TokenStatusDisabled
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if common.RedisEnabled {
		if err := cacheDeleteToken(token.Key); err != nil {
			return nil, err
		}
	}
	return &token, nil
}
