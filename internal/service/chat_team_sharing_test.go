package service

import (
	"testing"

	"gorm.io/gorm"

	"ragflow/internal/dao"
	"ragflow/internal/entity"
)

// createTeamSharingTestTenant creates a joinable tenant row: HasChatTeamPermission
// resolves team membership via TenantDAO.GetJoinedTenantsByUserID, which inner-joins
// tenant (status="1") with user_tenant (role="normal", status="1"), so both rows must
// exist for a team-shared chat to become visible to a joined member.
func createTeamSharingTestTenant(t *testing.T, db *gorm.DB, id string) {
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

// createChatTeamSharingTestChat creates a chat with an explicit permission,
// mirroring createChatListTestChat but for tests that need to control
// permission="me" vs permission="team".
func createChatTeamSharingTestChat(t *testing.T, db *gorm.DB, id, tenantID, name, permission string) {
	t.Helper()

	status := string(entity.StatusValid)
	chat := &entity.Chat{
		ID:           id,
		TenantID:     tenantID,
		Name:         &name,
		LLMID:        "model-a",
		LLMSetting:   entity.JSONMap{},
		PromptType:   "simple",
		PromptConfig: entity.JSONMap{},
		KBIDs:        entity.JSONSlice{},
		Status:       &status,
		Permission:   permission,
	}
	if err := db.Create(chat).Error; err != nil {
		t.Fatalf("failed to create chat: %v", err)
	}
}

// TestChatServiceListChatsSurfacesTeamSharedChats verifies that ListChats with
// no explicit owner_ids surfaces a teammate's permission="team" chat alongside
// the caller's own chats, and does not surface a teammate's permission="me"
// chat.
func TestChatServiceListChatsSurfacesTeamSharedChats(t *testing.T) {
	db := setupChatListTestDB(t)

	if err := db.Create(&entity.User{
		ID:       "tenant-2",
		Nickname: "team owner",
		Email:    "tenant-2@test.com",
		Status:   sptr("1"),
	}).Error; err != nil {
		t.Fatalf("failed to create tenant user: %v", err)
	}
	if err := db.Create(&entity.UserTenant{
		ID:        "rel-1",
		UserID:    "user-1",
		TenantID:  "tenant-2",
		Role:      "normal",
		InvitedBy: "tenant-2",
		Status:    sptr("1"),
	}).Error; err != nil {
		t.Fatalf("failed to create user tenant relation: %v", err)
	}

	createChatTeamSharingTestChat(t, db, "chat-own", "user-1", "own_chat", string(entity.TenantPermissionMe))
	createChatTeamSharingTestChat(t, db, "chat-team-shared", "tenant-2", "team_shared_chat", string(entity.TenantPermissionTeam))
	createChatTeamSharingTestChat(t, db, "chat-team-private", "tenant-2", "team_private_chat", string(entity.TenantPermissionMe))

	svc := NewChatService()
	ctx := t.Context()
	result, err := svc.ListChats(ctx, "user-1", "1", "", 0, 0, "create_time", true, nil)
	if err != nil {
		t.Fatalf("ListChats failed: %v", err)
	}

	if result.Total != 2 || len(result.Chats) != 2 {
		t.Fatalf("expected 2 visible chats (own + team-shared), got total=%d len=%d", result.Total, len(result.Chats))
	}

	seen := make(map[string]bool)
	for _, chat := range result.Chats {
		seen[chat.ID] = true
	}
	if !seen["chat-own"] {
		t.Fatal("expected caller's own chat to be visible")
	}
	if !seen["chat-team-shared"] {
		t.Fatal("expected teammate's permission=team chat to be visible")
	}
	if seen["chat-team-private"] {
		t.Fatal("expected teammate's permission=me chat to stay hidden")
	}
}

func TestChatServiceGetOwnedValidChatAllowsTeamSharedChat(t *testing.T) {
	db := setupChatListTestDB(t)
	createTeamSharingTestTenant(t, db, "tenant-2")
	if err := db.Create(&entity.UserTenant{
		ID:        "rel-1",
		UserID:    "user-1",
		TenantID:  "tenant-2",
		Role:      "normal",
		InvitedBy: "tenant-2",
		Status:    sptr("1"),
	}).Error; err != nil {
		t.Fatalf("failed to create user tenant relation: %v", err)
	}

	createChatTeamSharingTestChat(t, db, "chat-team", "tenant-2", "team_chat", string(entity.TenantPermissionTeam))
	createChatTeamSharingTestChat(t, db, "chat-private", "tenant-2", "private_chat", string(entity.TenantPermissionMe))

	svc := NewChatService()
	ctx := t.Context()

	if _, err := svc.getOwnedValidChat(ctx, "user-1", "chat-team"); err != nil {
		t.Fatalf("expected team member to access team-shared chat, got %v", err)
	}
	if _, err := svc.getOwnedValidChat(ctx, "user-1", "chat-private"); err == nil {
		t.Fatal("expected team member to be denied access to a permission=me chat")
	}
}

func TestChatServiceUpdateChatAllowsTeamSharedChat(t *testing.T) {
	db := setupChatRESTUpdateServiceTestDB(t)
	createTeamSharingTestTenant(t, db, "tenant-2")
	if err := db.Create(&entity.UserTenant{
		ID:        "rel-1",
		UserID:    "user-1",
		TenantID:  "tenant-2",
		Role:      "normal",
		InvitedBy: "tenant-2",
		Status:    sptr("1"),
	}).Error; err != nil {
		t.Fatalf("failed to create user tenant relation: %v", err)
	}

	createChatTeamSharingTestChat(t, db, "chat-team", "tenant-2", "team_chat", string(entity.TenantPermissionTeam))
	createChatTeamSharingTestChat(t, db, "chat-private", "tenant-2", "private_chat", string(entity.TenantPermissionMe))

	svc := NewChatService()
	ctx := t.Context()

	if _, err := svc.UpdateChat(ctx, "user-1", "chat-team", map[string]interface{}{
		"name": "renamed team chat",
	}); err != nil {
		t.Fatalf("expected team member to update team-shared chat, got %v", err)
	}

	if _, err := svc.UpdateChat(ctx, "user-1", "chat-private", map[string]interface{}{
		"name": "renamed private chat",
	}); err == nil {
		t.Fatal("expected team member to be denied updating a permission=me chat")
	}
}

func TestChatServiceDeleteChatAllowsTeamSharedChat(t *testing.T) {
	db := setupChatDeleteServiceTestDB(t)
	if err := db.AutoMigrate(&entity.Tenant{}, &entity.UserTenant{}); err != nil {
		t.Fatalf("failed to migrate tenant/user_tenant: %v", err)
	}
	createTeamSharingTestTenant(t, db, "tenant-2")
	if err := db.Create(&entity.UserTenant{
		ID:        "rel-1",
		UserID:    "user-1",
		TenantID:  "tenant-2",
		Role:      "normal",
		InvitedBy: "tenant-2",
		Status:    sptr("1"),
	}).Error; err != nil {
		t.Fatalf("failed to create user tenant relation: %v", err)
	}

	createChatTeamSharingTestChat(t, db, "chat-team", "tenant-2", "team_chat", string(entity.TenantPermissionTeam))
	createChatTeamSharingTestChat(t, db, "chat-private", "tenant-2", "private_chat", string(entity.TenantPermissionMe))

	svc := NewChatService()
	ctx := t.Context()

	if err := svc.DeleteChat(ctx, "user-1", "chat-team"); err != nil {
		t.Fatalf("expected team member to delete a team-shared chat, got %v", err)
	}
	chat, err := svc.chatDAO.GetByID(ctx, dao.DB, "chat-team")
	if err != nil {
		t.Fatalf("failed to fetch chat-team: %v", err)
	}
	if chat.Status == nil || *chat.Status != string(entity.StatusInvalid) {
		t.Fatalf("expected chat-team to be soft-deleted, got %+v", chat.Status)
	}

	if err := svc.DeleteChat(ctx, "user-1", "chat-private"); err == nil {
		t.Fatal("expected team member to be denied deleting a permission=me chat")
	}
}

func TestChatServiceGetChatAllowsTeamSharedChatAndDeniesPrivate(t *testing.T) {
	db := setupChatListTestDB(t)
	createTeamSharingTestTenant(t, db, "tenant-2")
	if err := db.Create(&entity.UserTenant{
		ID:        "rel-1",
		UserID:    "user-1",
		TenantID:  "tenant-2",
		Role:      "normal",
		InvitedBy: "tenant-2",
		Status:    sptr("1"),
	}).Error; err != nil {
		t.Fatalf("failed to create user tenant relation: %v", err)
	}

	createChatTeamSharingTestChat(t, db, "chat-team", "tenant-2", "team_chat", string(entity.TenantPermissionTeam))
	createChatTeamSharingTestChat(t, db, "chat-private", "tenant-2", "private_chat", string(entity.TenantPermissionMe))

	svc := NewChatService()
	ctx := t.Context()

	resp, err := svc.GetChat(ctx, "user-1", "chat-team")
	if err != nil {
		t.Fatalf("expected team member to read a team-shared chat, got %v", err)
	}
	if resp.ID != "chat-team" {
		t.Fatalf("expected chat-team, got %+v", resp.ID)
	}

	if _, err := svc.GetChat(ctx, "user-1", "chat-private"); err == nil || err.Error() != "no authorization" {
		t.Fatalf("expected no authorization for a permission=me chat, got %v", err)
	}

	if _, err := svc.GetChat(ctx, "user-1", "does-not-exist"); err == nil || err.Error() != "chat not found" {
		t.Fatalf("expected chat not found for a missing chat, got %v", err)
	}
}
