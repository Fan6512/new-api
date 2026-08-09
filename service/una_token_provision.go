package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const UnaTokenProvisionContractVersion = "una-token-provision.v1"

var (
	ErrUnaProvisionConflict = errors.New("una token provision idempotency conflict")
	ErrUnaRecoveryExpired   = errors.New("una token provision recovery expired")
	ErrUnaProvisionInvalid  = errors.New("una token provision request invalid")
)

type UnaRecoveryExpiredError struct {
	ExternalID string
}

func (e *UnaRecoveryExpiredError) Error() string { return ErrUnaRecoveryExpired.Error() }

func (e *UnaRecoveryExpiredError) Unwrap() error { return ErrUnaRecoveryExpired }

type UnaTokenProvisioner struct {
	DB          *gorm.DB
	KEK         []byte
	KEKVersion  string
	RecoveryTTL time.Duration
	Now         func() time.Time
	Random      io.Reader
	GenerateKey func() (string, error)
}

func NewUnaTokenProvisionerFromEnv() (*UnaTokenProvisioner, error) {
	encoded := strings.TrimSpace(os.Getenv("UNA_TOKEN_RECOVERY_KEK_B64"))
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return nil, errors.New("UNA_TOKEN_RECOVERY_KEK_B64 must decode to exactly 32 bytes")
	}
	version := strings.TrimSpace(os.Getenv("UNA_TOKEN_RECOVERY_KEK_VERSION"))
	if version == "" || len(version) > 32 {
		return nil, errors.New("UNA_TOKEN_RECOVERY_KEK_VERSION is required and must be at most 32 characters")
	}
	ttl := 24 * time.Hour
	if raw := strings.TrimSpace(os.Getenv("UNA_TOKEN_RECOVERY_TTL")); raw != "" {
		ttl, err = time.ParseDuration(raw)
		if err != nil {
			return nil, errors.New("UNA_TOKEN_RECOVERY_TTL is invalid")
		}
	}
	if ttl < 5*time.Minute || ttl > 72*time.Hour {
		return nil, errors.New("UNA_TOKEN_RECOVERY_TTL must be between 5m and 72h")
	}
	return &UnaTokenProvisioner{DB: model.DB, KEK: key, KEKVersion: version, RecoveryTTL: ttl, Now: time.Now, Random: rand.Reader, GenerateKey: common.GenerateKey}, nil
}

func (p *UnaTokenProvisioner) Create(ctx context.Context, idempotencyKey string, req dto.UnaTokenProvisionRequest) (dto.UnaTokenProvisionResponse, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	req.Name = strings.TrimSpace(req.Name)
	if err := validateUnaProvisionRequest(idempotencyKey, req); err != nil {
		return dto.UnaTokenProvisionResponse{}, err
	}
	if p == nil || p.DB == nil || len(p.KEK) != 32 || p.KEKVersion == "" || p.RecoveryTTL <= 0 || p.Now == nil || p.Random == nil || p.GenerateKey == nil {
		return dto.UnaTokenProvisionResponse{}, errors.New("una token provisioner is not configured")
	}
	fingerprint, err := unaRequestFingerprint(req)
	if err != nil {
		return dto.UnaTokenProvisionResponse{}, err
	}
	if existing, err := p.find(ctx, idempotencyKey); err == nil {
		return p.replay(existing, fingerprint)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return dto.UnaTokenProvisionResponse{}, err
	}

	var created model.UnaTokenProvision
	createdNew := false
	err = p.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Token bodies are credentials. Suppress interpolated SQL logging for the entire
		// atomic write even when the parent DB is running in debug or slow-query mode.
		tx = tx.Session(&gorm.Session{Logger: logger.Discard})
		var existing model.UnaTokenProvision
		if err := tx.Where("idempotency_key = ?", idempotencyKey).First(&existing).Error; err == nil {
			created = existing
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		randomPart, err := p.GenerateKey()
		if err != nil {
			return errors.New("generate una token key")
		}
		// 持久化、恢复和对外交付必须使用同一份原始凭据，避免跨服务前缀漂移。
		raw := "sk-una-" + randomPart
		storageKey := strings.TrimPrefix(raw, "sk-")
		now := p.Now().UTC()
		token := model.Token{UserId: req.OwnerUserID, Name: req.Name, Key: storageKey, Status: req.Status, CreatedTime: now.Unix(), AccessedTime: now.Unix(), ExpiredTime: -1, RemainQuota: req.Quota, UnlimitedQuota: false}
		if err := tx.Create(&token).Error; err != nil {
			return err
		}
		nonce, ciphertext, err := p.encrypt(raw, idempotencyKey, fingerprint, token.Id)
		if err != nil {
			return err
		}
		created = model.UnaTokenProvision{IdempotencyKey: idempotencyKey, RequestFingerprint: fingerprint, TokenId: token.Id, EncryptedKeyBody: ciphertext, Nonce: nonce, KekVersion: p.KEKVersion, RecoverableUntil: now.Add(p.RecoveryTTL).Unix(), CreatedTime: now.Unix(), UpdatedTime: now.Unix()}
		if err := tx.Create(&created).Error; err != nil {
			return err
		}
		createdNew = true
		return nil
	})
	if err != nil {
		if existing, findErr := p.find(ctx, idempotencyKey); findErr == nil {
			return p.replay(existing, fingerprint)
		}
		return dto.UnaTokenProvisionResponse{}, err
	}
	return p.replayWithFlag(created, fingerprint, !createdNew)
}

func (p *UnaTokenProvisioner) Recover(ctx context.Context, idempotencyKey string, req dto.UnaTokenProvisionRequest) (dto.UnaTokenProvisionResponse, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	req.Name = strings.TrimSpace(req.Name)
	if err := validateUnaProvisionRequest(idempotencyKey, req); err != nil {
		return dto.UnaTokenProvisionResponse{}, err
	}
	if p == nil || p.DB == nil || len(p.KEK) != 32 || p.KEKVersion == "" || p.Now == nil {
		return dto.UnaTokenProvisionResponse{}, errors.New("una token provisioner is not configured")
	}
	fingerprint, err := unaRequestFingerprint(req)
	if err != nil {
		return dto.UnaTokenProvisionResponse{}, err
	}
	existing, err := p.find(ctx, idempotencyKey)
	if err != nil {
		return dto.UnaTokenProvisionResponse{}, err
	}
	return p.replay(existing, fingerprint)
}

func (p *UnaTokenProvisioner) Disable(ctx context.Context, tokenID int) (dto.UnaTokenDisableResponse, error) {
	if tokenID <= 0 {
		return dto.UnaTokenDisableResponse{}, ErrUnaProvisionInvalid
	}
	token, err := model.DisableUnaTokenByID(ctx, tokenID)
	if err != nil {
		return dto.UnaTokenDisableResponse{}, err
	}
	return dto.UnaTokenDisableResponse{ContractVersion: UnaTokenProvisionContractVersion, ExternalID: strconv.Itoa(token.Id), Status: "disabled"}, nil
}

func (p *UnaTokenProvisioner) find(ctx context.Context, key string) (model.UnaTokenProvision, error) {
	var provision model.UnaTokenProvision
	err := p.DB.WithContext(ctx).Where("idempotency_key = ?", key).First(&provision).Error
	return provision, err
}

func (p *UnaTokenProvisioner) replay(provision model.UnaTokenProvision, fingerprint string) (dto.UnaTokenProvisionResponse, error) {
	return p.replayWithFlag(provision, fingerprint, true)
}

func (p *UnaTokenProvisioner) replayWithFlag(provision model.UnaTokenProvision, fingerprint string, replayed bool) (dto.UnaTokenProvisionResponse, error) {
	if provision.RequestFingerprint != fingerprint {
		return dto.UnaTokenProvisionResponse{}, ErrUnaProvisionConflict
	}
	if p.Now().UTC().Unix() > provision.RecoverableUntil {
		return dto.UnaTokenProvisionResponse{}, &UnaRecoveryExpiredError{ExternalID: strconv.Itoa(provision.TokenId)}
	}
	raw, err := p.decrypt(provision)
	if err != nil {
		return dto.UnaTokenProvisionResponse{}, errors.New("decrypt una token recovery material")
	}
	prefixLen := 12
	if len(raw) < prefixLen {
		prefixLen = len(raw)
	}
	return dto.UnaTokenProvisionResponse{ContractVersion: UnaTokenProvisionContractVersion, ExternalID: strconv.Itoa(provision.TokenId), RawKey: raw, DisplayPrefix: raw[:prefixLen], Last4: raw[len(raw)-4:], Replayed: replayed}, nil
}

func (p *UnaTokenProvisioner) encrypt(body, key, fingerprint string, tokenID int) ([]byte, []byte, error) {
	gcm, err := p.gcm()
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(p.Random, nonce); err != nil {
		return nil, nil, err
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(body), unaAAD(key, fingerprint, tokenID, p.KEKVersion))
	return nonce, ciphertext, nil
}

func (p *UnaTokenProvisioner) decrypt(provision model.UnaTokenProvision) (string, error) {
	if provision.KekVersion != p.KEKVersion {
		return "", errors.New("unknown KEK version")
	}
	gcm, err := p.gcm()
	if err != nil {
		return "", err
	}
	plain, err := gcm.Open(nil, provision.Nonce, provision.EncryptedKeyBody, unaAAD(provision.IdempotencyKey, provision.RequestFingerprint, provision.TokenId, provision.KekVersion))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (p *UnaTokenProvisioner) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(p.KEK)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func unaAAD(key, fingerprint string, tokenID int, version string) []byte {
	return []byte(fmt.Sprintf("%s\x00%s\x00%d\x00%s", key, fingerprint, tokenID, version))
}

func unaRequestFingerprint(req dto.UnaTokenProvisionRequest) (string, error) {
	encoded, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func validateUnaProvisionRequest(key string, req dto.UnaTokenProvisionRequest) error {
	if len(key) < 8 || len(key) > 128 || strings.ContainsAny(key, "\r\n") || req.OwnerUserID <= 0 || req.Name == "" || len([]rune(req.Name)) > 50 || req.Quota < 0 || (req.Status != common.TokenStatusEnabled && req.Status != common.TokenStatusDisabled) {
		return ErrUnaProvisionInvalid
	}
	return nil
}
