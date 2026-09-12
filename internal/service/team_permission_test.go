package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"ragflow/internal/dao"
	"ragflow/internal/entity"
)

func setupChatTeamPermissionTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		TranslateError: true,
	})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}

	if err = db.AutoMigrate(
		&entity.Chat{},
		&entity.Tenant{},
		&entity.UserTenant{},
	); err != nil {
		t.Fatalf("failed to migrate test schema: %v", err)
	}

	origDB := dao.DB
	dao.DB = db
	t.Cleanup(func() { dao.DB = origDB })

	return db
}

func createTeamPermissionTestTenant(t *testing.T, db *gorm.DB, id string) {
	t.Helper()
	status := string(entity.StatusValid)
	if err := db.Create(&entity.Tenant{
		ID:        id,
		LLMID:     "model-a",
		EmbdID:    "embd-a",
		ParserIDs: "naive",
		Status:    &status,
	}).Error; err != nil {
		t.Fatalf("failed to create tenant %s: %v", id, err)
	}
}

func joinTeamPermissionTestTenant(t *testing.T, db *gorm.DB, id, userID, tenantID string) {
	t.Helper()
	if err := db.Create(&entity.UserTenant{
		ID:        id,
		UserID:    userID,
		TenantID:  tenantID,
		Role:      "normal",
		InvitedBy: tenantID,
		Status:    sptr("1"),
	}).Error; err != nil {
		t.Fatalf("failed to create user_tenant relation: %v", err)
	}
}

func TestHasChatTeamPermission(t *testing.T) {
	db := setupChatTeamPermissionTestDB(t)
	tenantDAO := dao.NewTenantDAO()
	ctx := t.Context()

	createTeamPermissionTestTenant(t, db, "owner-tenant")
	createTeamPermissionTestTenant(t, db, "outsider-tenant")
	joinTeamPermissionTestTenant(t, db, "rel-1", "member-user", "owner-tenant")

	t.Run("nil chat is denied", func(t *testing.T) {
		if HasChatTeamPermission(ctx, nil, "anyone", tenantDAO) {
			t.Fatal("expected nil chat to be denied")
		}
	})

	t.Run("owner tenant is always allowed", func(t *testing.T) {
		chat := &entity.Chat{TenantID: "owner-tenant", Permission: string(entity.TenantPermissionMe)}
		if !HasChatTeamPermission(ctx, chat, "owner-tenant", tenantDAO) {
			t.Fatal("expected owning tenant to be allowed regardless of permission")
		}
	})

	t.Run("team-shared and joined is allowed", func(t *testing.T) {
		chat := &entity.Chat{TenantID: "owner-tenant", Permission: string(entity.TenantPermissionTeam)}
		if !HasChatTeamPermission(ctx, chat, "member-user", tenantDAO) {
			t.Fatal("expected joined team member to be allowed on a team-shared chat")
		}
	})

	t.Run("team-shared but not joined is denied", func(t *testing.T) {
		chat := &entity.Chat{TenantID: "owner-tenant", Permission: string(entity.TenantPermissionTeam)}
		if HasChatTeamPermission(ctx, chat, "stranger-user", tenantDAO) {
			t.Fatal("expected a non-member to be denied even on a team-shared chat")
		}
	})

	t.Run("not team-shared is denied for a joined member", func(t *testing.T) {
		chat := &entity.Chat{TenantID: "owner-tenant", Permission: string(entity.TenantPermissionMe)}
		if HasChatTeamPermission(ctx, chat, "member-user", tenantDAO) {
			t.Fatal("expected a joined member to be denied on a permission=me chat")
		}
	})

	t.Run("team-shared but joined to a different tenant is denied", func(t *testing.T) {
		chat := &entity.Chat{TenantID: "outsider-tenant", Permission: string(entity.TenantPermissionTeam)}
		if HasChatTeamPermission(ctx, chat, "member-user", tenantDAO) {
			t.Fatal("expected membership in an unrelated tenant to be denied")
		}
	})
}
