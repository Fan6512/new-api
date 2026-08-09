package service

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func newUnaProvisionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.Token{}, &model.UnaTokenProvision{}); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	previousDB := model.DB
	previousRedisEnabled := common.RedisEnabled
	model.DB = db
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		common.RedisEnabled = previousRedisEnabled
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func newUnaProvisionerForTest(t *testing.T) (*UnaTokenProvisioner, *gorm.DB, *time.Time) {
	t.Helper()
	db := newUnaProvisionTestDB(t)
	now := time.Date(2026, 7, 12, 4, 0, 0, 0, time.UTC)
	provisioner := &UnaTokenProvisioner{
		DB: db, KEK: bytes.Repeat([]byte{0x42}, 32), KEKVersion: "test-v1",
		RecoveryTTL: 24 * time.Hour, Now: func() time.Time { return now },
		Random:      bytes.NewReader(bytes.Repeat([]byte{0x24}, 256)),
		GenerateKey: func() (string, error) { return strings.Repeat("a", 48), nil },
	}
	return provisioner, db, &now
}

func validUnaProvisionRequest() dto.UnaTokenProvisionRequest {
	return dto.UnaTokenProvisionRequest{OwnerUserID: 7, Name: "Una key", Quota: 12345, Status: common.TokenStatusEnabled}
}

func TestUnaTokenProvisionCreateAndReplay(t *testing.T) {
	provisioner, db, _ := newUnaProvisionerForTest(t)
	ctx := context.Background()

	first, err := provisioner.Create(ctx, "idem-create-1", validUnaProvisionRequest())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if first.Replayed || first.ContractVersion != UnaTokenProvisionContractVersion || !strings.HasPrefix(first.RawKey, "sk-una-") {
		t.Fatalf("unexpected first response: %+v", first)
	}
	replay, err := provisioner.Create(ctx, "idem-create-1", validUnaProvisionRequest())
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.Replayed || replay.RawKey != first.RawKey || replay.ExternalID != first.ExternalID {
		t.Fatalf("unexpected replay response: %+v", replay)
	}

	var tokenCount, provisionCount int64
	if err := db.Model(&model.Token{}).Count(&tokenCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.UnaTokenProvision{}).Count(&provisionCount).Error; err != nil {
		t.Fatal(err)
	}
	if tokenCount != 1 || provisionCount != 1 {
		t.Fatalf("counts token=%d provision=%d, want 1/1", tokenCount, provisionCount)
	}
	var token model.Token
	if err := db.First(&token).Error; err != nil {
		t.Fatal(err)
	}
	if token.Key != first.RawKey || !strings.HasPrefix(token.Key, "sk-una-") {
		t.Fatalf("stored key %q does not match raw-key contract", token.Key)
	}
	var provision model.UnaTokenProvision
	if err := db.First(&provision).Error; err != nil {
		t.Fatal(err)
	}
	recovered, err := provisioner.decrypt(provision)
	if err != nil || recovered != first.RawKey {
		t.Fatalf("recovery material does not match raw key contract: err=%v", err)
	}
}

func TestUnaTokenProvisionConflictAndExpiry(t *testing.T) {
	provisioner, db, now := newUnaProvisionerForTest(t)
	ctx := context.Background()
	if _, err := provisioner.Create(ctx, "idem-conflict", validUnaProvisionRequest()); err != nil {
		t.Fatal(err)
	}
	changed := validUnaProvisionRequest()
	changed.Name = "different"
	if _, err := provisioner.Create(ctx, "idem-conflict", changed); !errors.Is(err, ErrUnaProvisionConflict) {
		t.Fatalf("conflict err=%v", err)
	}
	*now = now.Add(25 * time.Hour)
	if _, err := provisioner.Create(ctx, "idem-conflict", validUnaProvisionRequest()); !errors.Is(err, ErrUnaRecoveryExpired) {
		t.Fatalf("expiry err=%v", err)
	} else {
		var expired *UnaRecoveryExpiredError
		if !errors.As(err, &expired) || expired.ExternalID == "" {
			t.Fatalf("expiry error lacks external id: %v", err)
		}
	}
	var tokenCount int64
	_ = db.Model(&model.Token{}).Count(&tokenCount).Error
	if tokenCount != 1 {
		t.Fatalf("expiry created a second token: %d", tokenCount)
	}
}

func TestUnaTokenProvisionRecoverOnlyNeverCreates(t *testing.T) {
	provisioner, db, now := newUnaProvisionerForTest(t)
	ctx := context.Background()
	request := validUnaProvisionRequest()

	if _, err := provisioner.Recover(ctx, "idem-missing", request); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("missing recovery err=%v", err)
	}
	var tokenCount int64
	if err := db.Model(&model.Token{}).Count(&tokenCount).Error; err != nil || tokenCount != 0 {
		t.Fatalf("recover-only created token: count=%d err=%v", tokenCount, err)
	}

	created, err := provisioner.Create(ctx, "idem-recover", request)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := provisioner.Recover(ctx, "idem-recover", request)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered.Replayed || recovered.ExternalID != created.ExternalID || recovered.RawKey != created.RawKey {
		t.Fatalf("unexpected recovery: %+v", recovered)
	}
	changed := request
	changed.Name = "different"
	if _, err := provisioner.Recover(ctx, "idem-recover", changed); !errors.Is(err, ErrUnaProvisionConflict) {
		t.Fatalf("conflict recovery err=%v", err)
	}
	*now = now.Add(25 * time.Hour)
	if _, err := provisioner.Recover(ctx, "idem-recover", request); !errors.Is(err, ErrUnaRecoveryExpired) {
		t.Fatalf("expired recovery err=%v", err)
	}
	if err := db.Model(&model.Token{}).Count(&tokenCount).Error; err != nil || tokenCount != 1 {
		t.Fatalf("recover-only token count=%d err=%v", tokenCount, err)
	}
}

func TestUnaTokenProvisionRollsBackOnFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*UnaTokenProvisioner)
	}{
		{name: "key generation", mutate: func(p *UnaTokenProvisioner) {
			p.GenerateKey = func() (string, error) { return "", errors.New("secret failure") }
		}},
		{name: "nonce generation", mutate: func(p *UnaTokenProvisioner) { p.Random = bytes.NewReader(nil) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provisioner, db, _ := newUnaProvisionerForTest(t)
			tc.mutate(provisioner)
			response, err := provisioner.Create(context.Background(), "idem-rollback", validUnaProvisionRequest())
			if err == nil {
				t.Fatal("expected failure")
			}
			if strings.Contains(err.Error(), "sk-una-") || strings.Contains(err.Error(), strings.Repeat("a", 48)) || response.RawKey != "" {
				t.Fatalf("failure leaked raw key material: err=%v response=%+v", err, response)
			}
			var tokenCount, provisionCount int64
			_ = db.Model(&model.Token{}).Count(&tokenCount).Error
			_ = db.Model(&model.UnaTokenProvision{}).Count(&provisionCount).Error
			if tokenCount != 0 || provisionCount != 0 {
				t.Fatalf("transaction not rolled back: token=%d provision=%d", tokenCount, provisionCount)
			}
		})
	}
}

func TestUnaTokenProvisionRecoveryFailsClosed(t *testing.T) {
	provisioner, db, _ := newUnaProvisionerForTest(t)
	if _, err := provisioner.Create(context.Background(), "idem-tamper", validUnaProvisionRequest()); err != nil {
		t.Fatal(err)
	}
	var record model.UnaTokenProvision
	if err := db.First(&record).Error; err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), record.EncryptedKeyBody...)
	tampered[0] ^= 0xff
	if err := db.Model(&record).Update("encrypted_key_body", tampered).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := provisioner.Create(context.Background(), "idem-tamper", validUnaProvisionRequest()); err == nil || strings.Contains(err.Error(), "sk-una-") {
		t.Fatalf("tampered recovery did not fail closed: %v", err)
	}

	if err := db.Model(&record).Updates(map[string]any{"encrypted_key_body": record.EncryptedKeyBody, "kek_version": "unknown"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := provisioner.Create(context.Background(), "idem-tamper", validUnaProvisionRequest()); err == nil || strings.Contains(err.Error(), "sk-una-") {
		t.Fatalf("unknown KEK version did not fail closed: %v", err)
	}
}

func TestUnaTokenDisableIsIdempotent(t *testing.T) {
	provisioner, db, _ := newUnaProvisionerForTest(t)
	created, err := provisioner.Create(context.Background(), "idem-disable", validUnaProvisionRequest())
	if err != nil {
		t.Fatal(err)
	}
	var token model.Token
	if err := db.First(&token).Error; err != nil {
		t.Fatal(err)
	}
	first, err := provisioner.Disable(context.Background(), token.Id)
	if err != nil {
		t.Fatal(err)
	}
	second, err := provisioner.Disable(context.Background(), token.Id)
	if err != nil {
		t.Fatal(err)
	}
	if first.ExternalID != created.ExternalID || second.Status != "disabled" {
		t.Fatalf("unexpected disable responses: %+v %+v", first, second)
	}
	if err := db.First(&token, token.Id).Error; err != nil {
		t.Fatal(err)
	}
	if token.Status != common.TokenStatusDisabled {
		t.Fatalf("status=%d, want disabled", token.Status)
	}
}

func TestUnaTokenProvisionDoesNotLogRawKey(t *testing.T) {
	provisioner, db, _ := newUnaProvisionerForTest(t)
	var logs bytes.Buffer
	capturingLogger := gormlogger.New(log.New(&logs, "", 0), gormlogger.Config{LogLevel: gormlogger.Info, ParameterizedQueries: false})
	provisioner.DB = db.Session(&gorm.Session{Logger: capturingLogger})
	model.DB = provisioner.DB

	response, err := provisioner.Create(context.Background(), "idem-log-redaction", validUnaProvisionRequest())
	if err != nil {
		t.Fatal(err)
	}
	output := logs.String()
	if strings.Contains(output, response.RawKey) || strings.Contains(output, strings.TrimPrefix(response.RawKey, "sk-")) {
		t.Fatalf("SQL log leaked token credential: %s", output)
	}
}
