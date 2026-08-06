package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"fubao.ccvar.com/engine/internal/accounts"
	"fubao.ccvar.com/engine/internal/browsers"
	"fubao.ccvar.com/engine/internal/followinglive"
	"fubao.ccvar.com/engine/internal/license"
	"fubao.ccvar.com/engine/internal/redpacket"
	"fubao.ccvar.com/engine/internal/remotesync"
	"fubao.ccvar.com/engine/internal/rooms"
)

const protocolVersion = 1

type request struct {
	Version int             `json:"v"`
	ID      string          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	Version int       `json:"v"`
	ID      string    `json:"id,omitempty"`
	OK      bool      `json:"ok"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type event struct {
	Version int    `json:"v"`
	Event   string `json:"event"`
	Seq     uint64 `json:"seq"`
	Data    any    `json:"data"`
}

type engineStatus struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	GoVersion  string `json:"go_version"`
	Platform   string `json:"platform"`
	StartedAt  string `json:"started_at"`
	Protocol   int    `json:"protocol"`
	MonitorRun int    `json:"monitor_running"`
}

func main() {
	startedAt := time.Now()
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	accountStore, accountStoreErr := accounts.NewStore("")
	dataDir, dataDirErr := accounts.DefaultDataDir()
	var browserStore *browsers.Store
	browserStoreErr := dataDirErr
	if browserStoreErr == nil {
		browserStore, browserStoreErr = browsers.NewStore(dataDir)
	}
	followingLiveService := followinglive.NewService()
	var roomStore *rooms.Store
	roomStoreErr := dataDirErr
	if roomStoreErr == nil {
		roomStore, roomStoreErr = rooms.NewStore(dataDir)
	}
	var redPacketStore *redpacket.Store
	var redPacketParticipant *redpacket.Participant
	var pageParticipation *pageParticipationBroker
	redPacketStoreErr := dataDirErr
	if redPacketStoreErr == nil {
		redPacketStore, redPacketStoreErr = redpacket.NewStore(dataDir)
	}
	if redPacketStoreErr == nil && accountStoreErr == nil {
		redPacketStore.SetRequestRecorder(accountStore.RecordMonitoringRequest)
		// A native Douyin page context cannot survive a real client/engine
		// restart. Close stale tasks immediately so the frontend never reports
		// a historical Active flag as a currently running participation task.
		if reconciliation, err := redPacketStore.ReconcileParticipationTasksAfterRestart(time.Now()); err == nil {
			for _, accountID := range reconciliation.StoppedAccountIDs {
				_, _ = accountStore.SetRedPacketAPIEnabled(accountID, false)
			}
		}
	}
	if browserStoreErr == nil && accountStoreErr == nil {
		browserStore.SetCookieUpdater(func(accountID, rawCookie string) error {
			if _, err := accountStore.ReplaceCookie(accountID, rawCookie); err != nil {
				return err
			}
			if redPacketStoreErr == nil {
				refreshRunningMonitoringPool(accountStore, redPacketStore)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			_, err := accountStore.ValidateCookie(ctx, accountID, true)
			return err
		})
	}
	if redPacketStoreErr == nil && roomStoreErr == nil {
		redPacketStore.SetLiveResultHandler(func(roomID, status string, checkedAt time.Time) {
			recycled, err := roomStore.RecordLiveResult(roomID, status, checkedAt)
			if err != nil || !recycled {
				return
			}
			_ = redPacketStore.Stop("room_" + roomID)
			_ = redPacketStore.SyncRooms(roomStore.All())
		})
	}
	if redPacketStoreErr == nil && accountStoreErr == nil && browserStoreErr == nil {
		pageParticipation = newPageParticipationBroker(browserStore)
		redPacketParticipant = redpacket.NewPageParticipant(accountStore, pageParticipation, redPacketStore)
		redPacketParticipant.SetFollowMatcher(followingLiveService)
	}
	var remoteSyncManager *remotesync.Manager
	remoteSyncManagerErr := dataDirErr
	if remoteSyncManagerErr == nil {
		remoteSyncManager, remoteSyncManagerErr = remotesync.New(dataDir)
	}
	if redPacketStoreErr == nil && (redPacketParticipant != nil || remoteSyncManager != nil) {
		redPacketStore.SetEventHandler(func(event redpacket.Event) {
			if roomStoreErr == nil {
				detectedAt, err := time.Parse(time.RFC3339Nano, event.DetectedAt)
				if err != nil {
					detectedAt = time.Now()
				}
				roomID := strings.TrimSpace(event.RoomID)
				if roomID == "" {
					roomID = strings.TrimSpace(event.WebRID)
				}
				_ = roomStore.RecordRedPacketEvent(roomID, detectedAt)
			}
			if redPacketParticipant != nil {
				redPacketParticipant.HandleEvent(event)
			}
			if remoteSyncManager != nil {
				_ = remoteSyncManager.EnqueueEvent(event)
			}
		})
	}
	var licenseManager *license.Manager
	licenseManagerErr := dataDirErr
	if licenseManagerErr == nil {
		licenseManager, licenseManagerErr = license.New(dataDir, "0.1.0", license.Config{})
	}

	_ = encoder.Encode(event{
		Version: protocolVersion,
		Event:   "engine.ready",
		Seq:     1,
		Data: engineStatus{
			Name:      "fubao-engine",
			Version:   "0.1.0",
			GoVersion: runtime.Version(),
			Platform:  runtime.GOOS + "/" + runtime.GOARCH,
			StartedAt: startedAt.Format(time.RFC3339),
			Protocol:  protocolVersion,
		},
	})
	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	defer stopBackground()
	if accountStoreErr == nil && browserStoreErr == nil && roomStoreErr == nil {
		go runFollowingLiveSync(backgroundCtx, accountStore, browserStore, followingLiveService, roomStore, redPacketStore)
	}
	if accountStoreErr == nil && roomStoreErr == nil && redPacketStoreErr == nil {
		go runParticipationMonitorPrewarm(backgroundCtx, accountStore, roomStore, redPacketStore)
	}
	if remoteSyncManagerErr == nil {
		go remoteSyncManager.Run(backgroundCtx)
		if roomStoreErr == nil && redPacketStoreErr == nil {
			go runRemoteSyncSnapshots(backgroundCtx, remoteSyncManager, roomStore, redPacketStore, licenseManager)
		}
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			_ = encoder.Encode(response{
				Version: protocolVersion,
				OK:      false,
				Error:   &rpcError{Code: "invalid_json", Message: "请求不是有效 JSON"},
			})
			continue
		}

		if req.Version != protocolVersion {
			_ = encoder.Encode(response{
				Version: protocolVersion,
				ID:      req.ID,
				OK:      false,
				Error:   &rpcError{Code: "protocol_mismatch", Message: "IPC 协议版本不兼容"},
			})
			continue
		}

		switch req.Method {
		case "system.ping":
			_ = encoder.Encode(response{
				Version: protocolVersion,
				ID:      req.ID,
				OK:      true,
				Result:  map[string]any{"pong": true, "at": time.Now().Format(time.RFC3339Nano)},
			})
		case "engine.status":
			_ = encoder.Encode(response{
				Version: protocolVersion,
				ID:      req.ID,
				OK:      true,
				Result: engineStatus{
					Name:      "fubao-engine",
					Version:   "0.1.0",
					GoVersion: runtime.Version(),
					Platform:  runtime.GOOS + "/" + runtime.GOARCH,
					StartedAt: startedAt.Format(time.RFC3339),
					Protocol:  protocolVersion,
				},
			})
		case "remote_sync.status":
			if remoteSyncManagerErr != nil {
				writeError(encoder, req.ID, "remote_sync_unavailable", remoteSyncManagerErr.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: remoteSyncManager.Status()})
		case "remote_sync.configure":
			if remoteSyncManagerErr != nil {
				writeError(encoder, req.ID, "remote_sync_unavailable", remoteSyncManagerErr.Error())
				continue
			}
			var params struct {
				Enabled         bool   `json:"enabled"`
				EnrollmentToken string `json:"enrollment_token"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeError(encoder, req.ID, "invalid_params", "远程同步配置参数无效")
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			result, err := remoteSyncManager.Configure(ctx, params.Enabled, params.EnrollmentToken)
			cancel()
			if err != nil {
				writeError(encoder, req.ID, "remote_sync_configure_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: result})
		case "license.status":
			if licenseManagerErr != nil {
				writeError(encoder, req.ID, "license_unavailable", licenseManagerErr.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: licenseManager.Status()})
		case "license.activate":
			if licenseManagerErr != nil {
				writeError(encoder, req.ID, "license_unavailable", licenseManagerErr.Error())
				continue
			}
			var params struct {
				LicenseKey  string `json:"license_key"`
				MachineName string `json:"machine_name"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeError(encoder, req.ID, "invalid_params", "激活参数无效")
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			result := licenseManager.Activate(ctx, params.LicenseKey, params.MachineName)
			cancel()
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: result})
		case "license.refresh":
			if licenseManagerErr != nil {
				writeError(encoder, req.ID, "license_unavailable", licenseManagerErr.Error())
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			result := licenseManager.Refresh(ctx)
			cancel()
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: result})
		case "license.deactivate":
			if licenseManagerErr != nil {
				writeError(encoder, req.ID, "license_unavailable", licenseManagerErr.Error())
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			result := licenseManager.Deactivate(ctx)
			cancel()
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: result})
		case "account.list":
			if accountStoreErr != nil {
				writeError(encoder, req.ID, "account_store_unavailable", accountStoreErr.Error())
				continue
			}
			var params struct {
				Role accounts.Role `json:"role"`
			}
			if len(req.Params) > 0 {
				if err := json.Unmarshal(req.Params, &params); err != nil {
					writeError(encoder, req.ID, "invalid_params", "账号列表参数无效")
					continue
				}
			}
			_ = encoder.Encode(response{
				Version: protocolVersion,
				ID:      req.ID,
				OK:      true,
				Result:  accountStore.List(params.Role),
			})
		case "account.participation_group.list":
			if accountStoreErr != nil {
				writeError(encoder, req.ID, "account_store_unavailable", accountStoreErr.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: accountStore.ListParticipationGroups()})
		case "account.participation_group.create":
			if accountStoreErr != nil {
				writeError(encoder, req.ID, "account_store_unavailable", accountStoreErr.Error())
				continue
			}
			var params struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeError(encoder, req.ID, "invalid_params", "参与账号分组参数无效")
				continue
			}
			group, err := accountStore.CreateParticipationGroup(params.Name)
			if err != nil {
				writeError(encoder, req.ID, "participation_group_create_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: group})
		case "account.participation_group.set":
			if accountStoreErr != nil {
				writeError(encoder, req.ID, "account_store_unavailable", accountStoreErr.Error())
				continue
			}
			var params struct {
				AccountID string `json:"account_id"`
				GroupID   string `json:"group_id"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.AccountID) == "" {
				writeError(encoder, req.ID, "invalid_params", "参与账号改组参数无效")
				continue
			}
			account, err := accountStore.SetParticipationGroup(params.AccountID, params.GroupID)
			if err != nil {
				writeError(encoder, req.ID, "participation_group_set_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: account})
		case "account.migrate_legacy":
			if accountStoreErr != nil {
				writeError(encoder, req.ID, "account_store_unavailable", accountStoreErr.Error())
				continue
			}
			var params struct {
				LegacyDir string `json:"legacy_dir"`
			}
			if len(req.Params) > 0 {
				if err := json.Unmarshal(req.Params, &params); err != nil {
					writeError(encoder, req.ID, "invalid_params", "迁移参数无效")
					continue
				}
			}
			result, err := accountStore.MigrateLegacy(params.LegacyDir)
			if err != nil {
				writeError(encoder, req.ID, "legacy_migration_failed", err.Error())
				continue
			}
			if redPacketStoreErr == nil {
				refreshRunningMonitoringPool(accountStore, redPacketStore)
			}
			_ = encoder.Encode(response{
				Version: protocolVersion,
				ID:      req.ID,
				OK:      true,
				Result:  result,
			})
		case "account.import_cookies":
			if accountStoreErr != nil {
				writeError(encoder, req.ID, "account_store_unavailable", accountStoreErr.Error())
				continue
			}
			var params struct {
				Role    accounts.Role           `json:"role"`
				GroupID string                  `json:"group_id"`
				Sources []accounts.ImportSource `json:"sources"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || (params.Role != accounts.RoleMonitoring && params.Role != accounts.RoleParticipation) {
				writeError(encoder, req.ID, "invalid_params", "账号导入参数无效")
				continue
			}
			records, invalidSources := accounts.ParseImportSources(params.Sources)
			if len(records) == 0 {
				writeError(encoder, req.ID, "account_import_empty", "没有识别到有效的抖音登录 Cookie")
				continue
			}
			createdCount := 0
			mergedCount := 0
			failedCount := 0
			// Small/manual imports resolve identity and wallet online. Large batch
			// imports stay fast and only persist the Cookie; the list can refresh
			// wallets later without blocking the whole import.
			resolveOnline := len(records) <= 20
			identityCtx, identityCancel := context.WithTimeout(context.Background(), 30*time.Second)
			for _, record := range records {
				// Single Cookie/browser exports usually have no account metadata.
				// Resolve a friendly identity for small explicit imports, while a
				// large legacy batch remains fast and keeps its file metadata.
				if resolveOnline && strings.TrimSpace(record.UserID) == "" {
					if identity, err := accounts.ResolveDouyinIdentity(identityCtx, record.Cookie); err == nil {
						record.Nickname = identity.Nickname
						record.UserID = identity.UserID
						record.SecUID = identity.SecUID
					}
				}
				view, created, err := accountStore.UpsertImportedCookieWithGroup(record.Cookie, record.Nickname, record.UserID, record.SecUID, params.Role, params.GroupID)
				if err != nil {
					failedCount++
					continue
				}
				if created {
					createdCount++
				} else {
					mergedCount++
				}
				// Participation diamond display requires a native wallet snapshot.
				// Without diamond_status=valid the UI correctly shows “暂无”, even when
				// the live account actually has diamonds.
				if resolveOnline && params.Role == accounts.RoleParticipation && strings.TrimSpace(view.ID) != "" {
					walletCtx, walletCancel := context.WithTimeout(identityCtx, 8*time.Second)
					_, _ = accountStore.RefreshRedPacketWalletBalance(walletCtx, view.ID)
					walletCancel()
				}
			}
			identityCancel()
			if params.Role == accounts.RoleMonitoring && redPacketStoreErr == nil {
				refreshRunningMonitoringPool(accountStore, redPacketStore)
			}
			_ = encoder.Encode(response{
				Version: protocolVersion,
				ID:      req.ID,
				OK:      true,
				Result: map[string]interface{}{
					"imported":        createdCount,
					"merged":          mergedCount,
					"failed":          failedCount,
					"progress":        100,
					"invalid_sources": invalidSources,
					"total":           len(accountStore.List(params.Role)),
				},
			})
		case "account.add_role":
			if accountStoreErr != nil {
				writeError(encoder, req.ID, "account_store_unavailable", accountStoreErr.Error())
				continue
			}
			var params struct {
				AccountID string        `json:"account_id"`
				Role      accounts.Role `json:"role"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeError(encoder, req.ID, "invalid_params", "账号分类参数无效")
				continue
			}
			result, err := accountStore.AddRole(params.AccountID, params.Role)
			if err != nil {
				writeError(encoder, req.ID, "account_role_failed", err.Error())
				continue
			}
			if params.Role == accounts.RoleMonitoring && redPacketStoreErr == nil {
				refreshRunningMonitoringPool(accountStore, redPacketStore)
			}
			_ = encoder.Encode(response{
				Version: protocolVersion,
				ID:      req.ID,
				OK:      true,
				Result:  result,
			})
		case "account.remove_role":
			if accountStoreErr != nil {
				writeError(encoder, req.ID, "account_store_unavailable", accountStoreErr.Error())
				continue
			}
			var params struct {
				AccountID string        `json:"account_id"`
				Role      accounts.Role `json:"role"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeError(encoder, req.ID, "invalid_params", "账号分类参数无效")
				continue
			}
			result, err := accountStore.RemoveRole(params.AccountID, params.Role)
			if err != nil {
				writeError(encoder, req.ID, "account_role_failed", err.Error())
				continue
			}
			if params.Role == accounts.RoleMonitoring && redPacketStoreErr == nil {
				refreshRunningMonitoringPool(accountStore, redPacketStore)
			}
			_ = encoder.Encode(response{
				Version: protocolVersion,
				ID:      req.ID,
				OK:      true,
				Result:  result,
			})
		case "account.set_red_packet_api_enabled":
			if accountStoreErr != nil {
				writeError(encoder, req.ID, "account_store_unavailable", accountStoreErr.Error())
				continue
			}
			var params struct {
				AccountID string `json:"account_id"`
				Enabled   bool   `json:"enabled"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.AccountID) == "" {
				writeError(encoder, req.ID, "invalid_params", "红包接口参与开关参数无效")
				continue
			}
			result, err := accountStore.SetRedPacketAPIEnabled(params.AccountID, params.Enabled)
			if err != nil {
				writeError(encoder, req.ID, "account_red_packet_api_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: result})
		case "account.delete":
			if accountStoreErr != nil {
				writeError(encoder, req.ID, "account_store_unavailable", accountStoreErr.Error())
				continue
			}
			var params struct {
				AccountID string `json:"account_id"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeError(encoder, req.ID, "invalid_params", "删除账号参数无效")
				continue
			}
			if err := accountStore.Delete(params.AccountID); err != nil {
				writeError(encoder, req.ID, "account_delete_failed", err.Error())
				continue
			}
			if redPacketStoreErr == nil {
				refreshRunningMonitoringPool(accountStore, redPacketStore)
			}
			_ = encoder.Encode(response{
				Version: protocolVersion,
				ID:      req.ID,
				OK:      true,
				Result:  map[string]bool{"deleted": true},
			})
		case "account.validate_cookie":
			if accountStoreErr != nil {
				writeError(encoder, req.ID, "account_store_unavailable", accountStoreErr.Error())
				continue
			}
			var params struct {
				AccountID string        `json:"account_id"`
				Role      accounts.Role `json:"role"`
				Force     bool          `json:"force"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.AccountID) == "" {
				writeError(encoder, req.ID, "invalid_params", "CK 校验参数无效")
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			if params.Role == "" {
				params.Role = accounts.RoleParticipation
			}
			result, err := accountStore.ValidateCookieForRole(ctx, params.AccountID, params.Role, params.Force)
			// A valid participation CK should also refresh the wallet snapshot so
			// the account row can leave the “钻石 暂无” placeholder.
			if err == nil && params.Role == accounts.RoleParticipation && strings.EqualFold(strings.TrimSpace(result.Status), "valid") {
				walletCtx, walletCancel := context.WithTimeout(ctx, 8*time.Second)
				_, _ = accountStore.RefreshRedPacketWalletBalance(walletCtx, params.AccountID)
				walletCancel()
			}
			cancel()
			if err != nil {
				writeError(encoder, req.ID, "cookie_validation_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: result})
		case "account.refresh_wallet":
			if accountStoreErr != nil {
				writeError(encoder, req.ID, "account_store_unavailable", accountStoreErr.Error())
				continue
			}
			var params struct {
				AccountID string `json:"account_id"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.AccountID) == "" {
				writeError(encoder, req.ID, "invalid_params", "钱包刷新参数无效")
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			balance, err := accountStore.RefreshRedPacketWalletBalance(ctx, params.AccountID)
			cancel()
			if err != nil {
				writeError(encoder, req.ID, "wallet_refresh_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{
				Version: protocolVersion,
				ID:      req.ID,
				OK:      true,
				Result: map[string]any{
					"account_id":         params.AccountID,
					"diamond_balance":    balance.Diamond,
					"diamond_x10":        balance.DiamondX10,
					"diamond_checked_at": balance.CheckedAt,
					"diamond_status":     "valid",
				},
			})
		case "account.native_credential":
			if accountStoreErr != nil {
				writeError(encoder, req.ID, "account_store_unavailable", accountStoreErr.Error())
				continue
			}
			var params struct {
				AccountID string `json:"account_id"`
				Secret    string `json:"secret"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.AccountID) == "" {
				writeError(encoder, req.ID, "invalid_params", "原生账号凭据参数无效")
				continue
			}
			nativeSecret := strings.TrimSpace(os.Getenv("FUBAO_NATIVE_RPC_SECRET"))
			if nativeSecret == "" || params.Secret != nativeSecret {
				writeError(encoder, req.ID, "native_auth_failed", "原生账号凭据请求未授权")
				continue
			}
			credential, err := accountStore.Credential(params.AccountID)
			if err != nil {
				writeError(encoder, req.ID, "account_credential_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: map[string]string{
				"account_id": credential.AccountID, "account_name": credential.AccountName, "cookie": credential.Cookie,
			}})
		case "account.native_replace_cookie":
			if accountStoreErr != nil {
				writeError(encoder, req.ID, "account_store_unavailable", accountStoreErr.Error())
				continue
			}
			var params struct {
				AccountID string `json:"account_id"`
				Cookie    string `json:"cookie"`
				Secret    string `json:"secret"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.AccountID) == "" {
				writeError(encoder, req.ID, "invalid_params", "更新账号 CK 参数无效")
				continue
			}
			nativeSecret := strings.TrimSpace(os.Getenv("FUBAO_NATIVE_RPC_SECRET"))
			if nativeSecret == "" || params.Secret != nativeSecret {
				writeError(encoder, req.ID, "native_auth_failed", "更新账号 CK 请求未授权")
				continue
			}
			account, err := accountStore.ReplaceCookie(params.AccountID, params.Cookie)
			if err != nil {
				writeError(encoder, req.ID, "account_cookie_update_failed", err.Error())
				continue
			}
			if redPacketStoreErr == nil {
				refreshRunningMonitoringPool(accountStore, redPacketStore)
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: account})
		case "account.native_set_browser_login_state":
			if accountStoreErr != nil {
				writeError(encoder, req.ID, "account_store_unavailable", accountStoreErr.Error())
				continue
			}
			var params struct {
				AccountID            string `json:"account_id"`
				LoggedIn             bool   `json:"logged_in"`
				PromoteNativeSurface bool   `json:"promote_native_surface"`
				Secret               string `json:"secret"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.AccountID) == "" {
				writeError(encoder, req.ID, "invalid_params", "同步浏览器登录状态参数无效")
				continue
			}
			nativeSecret := strings.TrimSpace(os.Getenv("FUBAO_NATIVE_RPC_SECRET"))
			if nativeSecret == "" || params.Secret != nativeSecret {
				writeError(encoder, req.ID, "native_auth_failed", "同步浏览器登录状态请求未授权")
				continue
			}
			account, err := accountStore.SetBrowserLoginStateWithPromotion(params.AccountID, params.LoggedIn, params.PromoteNativeSurface)
			if err != nil {
				writeError(encoder, req.ID, "account_login_state_update_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: account})
		case "account.native_create_from_cookie":
			if accountStoreErr != nil {
				writeError(encoder, req.ID, "account_store_unavailable", accountStoreErr.Error())
				continue
			}
			var params struct {
				Cookie  string        `json:"cookie"`
				Role    accounts.Role `json:"role"`
				GroupID string        `json:"group_id"`
				Secret  string        `json:"secret"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.Cookie) == "" {
				writeError(encoder, req.ID, "invalid_params", "新增扫码账号参数无效")
				continue
			}
			nativeSecret := strings.TrimSpace(os.Getenv("FUBAO_NATIVE_RPC_SECRET"))
			if nativeSecret == "" || params.Secret != nativeSecret {
				writeError(encoder, req.ID, "native_auth_failed", "新增扫码账号请求未授权")
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			identity, err := accounts.ResolveDouyinIdentity(ctx, params.Cookie)
			cancel()
			if err != nil {
				writeError(encoder, req.ID, "account_identity_failed", err.Error())
				continue
			}
			account, created, err := accountStore.UpsertAuthenticatedCookieWithGroup(params.Cookie, identity.Nickname, identity.UserID, identity.SecUID, params.Role, params.GroupID)
			if err != nil {
				writeError(encoder, req.ID, "account_create_failed", err.Error())
				continue
			}
			if params.Role == accounts.RoleMonitoring && redPacketStoreErr == nil {
				refreshRunningMonitoringPool(accountStore, redPacketStore)
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: map[string]any{"account": account, "created": created}})
		case "browser.list":
			if browserStoreErr != nil {
				writeError(encoder, req.ID, "browser_store_unavailable", browserStoreErr.Error())
				continue
			}
			_ = encoder.Encode(response{
				Version: protocolVersion,
				ID:      req.ID,
				OK:      true,
				Result:  browserInstancesWithSurfaces(browserStore, accountStore, accountStoreErr),
			})
		case "browser.native_following_live":
			if accountStoreErr != nil {
				writeError(encoder, req.ID, "account_store_unavailable", accountStoreErr.Error())
				continue
			}
			if browserStoreErr != nil {
				writeError(encoder, req.ID, "browser_store_unavailable", browserStoreErr.Error())
				continue
			}
			var params struct {
				InstanceID string               `json:"instance_id"`
				Items      []followinglive.Item `json:"items"`
				Secret     string               `json:"secret"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.InstanceID) == "" {
				writeError(encoder, req.ID, "invalid_params", "原生关注直播参数无效")
				continue
			}
			nativeSecret := strings.TrimSpace(os.Getenv("FUBAO_NATIVE_RPC_SECRET"))
			if nativeSecret == "" || params.Secret != nativeSecret {
				writeError(encoder, req.ID, "native_auth_failed", "原生关注直播请求未授权")
				continue
			}
			accountID, err := browserStore.AccountID(params.InstanceID)
			if err != nil {
				writeError(encoder, req.ID, "browser_following_live_failed", err.Error())
				continue
			}
			credential, err := accountStore.ParticipationCredential(accountID)
			if err != nil {
				writeError(encoder, req.ID, "browser_account_invalid", err.Error())
				continue
			}
			result := followingLiveService.StoreNative(credential.AccountID, params.Items)
			if err := mergeFollowingLiveResult(roomStore, redPacketStore, credential, result); err != nil {
				writeError(encoder, req.ID, "browser_following_live_sync_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: result})
		case "browser.following_live":
			if accountStoreErr != nil {
				writeError(encoder, req.ID, "account_store_unavailable", accountStoreErr.Error())
				continue
			}
			if browserStoreErr != nil {
				writeError(encoder, req.ID, "browser_store_unavailable", browserStoreErr.Error())
				continue
			}
			var params struct {
				InstanceID string `json:"instance_id"`
				Force      bool   `json:"force"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.InstanceID) == "" {
				writeError(encoder, req.ID, "invalid_params", "关注直播参数无效")
				continue
			}
			accountID, err := browserStore.AccountID(params.InstanceID)
			if err != nil {
				writeError(encoder, req.ID, "browser_following_live_failed", err.Error())
				continue
			}
			credential, err := accountStore.ParticipationCredential(accountID)
			if err != nil {
				writeError(encoder, req.ID, "browser_account_invalid", err.Error())
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			result, err := followingLiveService.Fetch(ctx, credential.AccountID, credential.Cookie, params.Force)
			cancel()
			if err != nil {
				writeError(encoder, req.ID, "browser_following_live_failed", err.Error())
				continue
			}
			if !result.Stale {
				if err := mergeFollowingLiveResult(roomStore, redPacketStore, credential, result); err != nil {
					writeError(encoder, req.ID, "browser_following_live_sync_failed", err.Error())
					continue
				}
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: result})
		case "browser.capacity":
			if browserStoreErr != nil {
				writeError(encoder, req.ID, "browser_store_unavailable", browserStoreErr.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: browserStore.Capacity()})
		case "browser.runtime.acquire":
			if browserStoreErr != nil {
				writeError(encoder, req.ID, "browser_store_unavailable", browserStoreErr.Error())
				continue
			}
			var params struct {
				InstanceID string `json:"instance_id"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeError(encoder, req.ID, "invalid_params", "实例运行槽参数无效")
				continue
			}
			admission, err := browserStore.AcquireEmbedded(params.InstanceID)
			if err != nil {
				writeError(encoder, req.ID, "browser_runtime_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: admission})
		case "browser.runtime.release":
			if browserStoreErr != nil {
				writeError(encoder, req.ID, "browser_store_unavailable", browserStoreErr.Error())
				continue
			}
			var params struct {
				InstanceID string `json:"instance_id"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeError(encoder, req.ID, "invalid_params", "释放实例运行槽参数无效")
				continue
			}
			capacity, err := browserStore.ReleaseEmbedded(params.InstanceID)
			if err != nil {
				writeError(encoder, req.ID, "browser_runtime_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: capacity})
		case "browser.create":
			if accountStoreErr != nil {
				writeError(encoder, req.ID, "account_store_unavailable", accountStoreErr.Error())
				continue
			}
			if browserStoreErr != nil {
				writeError(encoder, req.ID, "browser_store_unavailable", browserStoreErr.Error())
				continue
			}
			var params struct {
				AccountID string `json:"account_id"`
				Name      string `json:"name"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeError(encoder, req.ID, "invalid_params", "创建浏览器实例参数无效")
				continue
			}
			credential, err := accountStore.ParticipationCredential(params.AccountID)
			if err != nil {
				writeError(encoder, req.ID, "browser_account_invalid", err.Error())
				continue
			}
			instance, err := browserStore.CreateWithLimit(
				credential.AccountID,
				credential.AccountName,
				params.Name,
				browserInstanceCreateLimit(licenseManager),
				browsers.SurfaceForAccountSource(credential.Source),
			)
			if err != nil {
				writeError(encoder, req.ID, "browser_create_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{
				Version: protocolVersion,
				ID:      req.ID,
				OK:      true,
				Result:  instance,
			})
		case "browser.open":
			if accountStoreErr != nil {
				writeError(encoder, req.ID, "account_store_unavailable", accountStoreErr.Error())
				continue
			}
			if browserStoreErr != nil {
				writeError(encoder, req.ID, "browser_store_unavailable", browserStoreErr.Error())
				continue
			}
			var params struct {
				InstanceID string `json:"instance_id"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeError(encoder, req.ID, "invalid_params", "打开浏览器实例参数无效")
				continue
			}
			accountID, err := browserStore.AccountID(params.InstanceID)
			if err != nil {
				writeError(encoder, req.ID, "browser_open_failed", err.Error())
				continue
			}
			credential, err := accountStore.ParticipationCredential(accountID)
			if err != nil {
				writeError(encoder, req.ID, "browser_account_invalid", err.Error())
				continue
			}
			// Keep surface in sync before launching so an upgraded rebind
			// account is not forced through external Chrome by a stale flag.
			browserStore.ApplyAccountSources(map[string]string{credential.AccountID: credential.Source})
			if browsers.SurfaceForAccountSource(credential.Source) != browsers.SurfaceExternalChrome {
				writeError(encoder, req.ID, "browser_open_failed", "该账号使用内嵌实例，请从卡片打开原生窗口")
				continue
			}
			instance, err := browserStore.Open(params.InstanceID, credential.Cookie)
			if err != nil {
				writeError(encoder, req.ID, "browser_open_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{
				Version: protocolVersion,
				ID:      req.ID,
				OK:      true,
				Result:  instance,
			})
		case "browser.repair_login":
			// Rescue-only Chrome CDP path for import accounts whose WKWebView
			// inject failed. Does not flip the card surface away from embedded;
			// extension cookie sync + credential_updated_at remounts the card.
			if accountStoreErr != nil {
				writeError(encoder, req.ID, "account_store_unavailable", accountStoreErr.Error())
				continue
			}
			if browserStoreErr != nil {
				writeError(encoder, req.ID, "browser_store_unavailable", browserStoreErr.Error())
				continue
			}
			var params struct {
				InstanceID string `json:"instance_id"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeError(encoder, req.ID, "invalid_params", "Chrome 登录修复参数无效")
				continue
			}
			accountID, err := browserStore.AccountID(params.InstanceID)
			if err != nil {
				writeError(encoder, req.ID, "browser_repair_failed", err.Error())
				continue
			}
			credential, err := accountStore.ParticipationCredential(accountID)
			if err != nil {
				writeError(encoder, req.ID, "browser_account_invalid", err.Error())
				continue
			}
			// Keep embedded as the default host surface; repair only opens Chrome.
			browserStore.ApplyAccountSources(map[string]string{credential.AccountID: credential.Source})
			instance, err := browserStore.Open(params.InstanceID, credential.Cookie)
			if err != nil {
				writeError(encoder, req.ID, "browser_repair_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{
				Version: protocolVersion,
				ID:      req.ID,
				OK:      true,
				Result:  instance,
			})
		case "browser.repair_stop":
			// Close the temporary Chrome repair session when the shell is
			// dismissed so the card does not stay stuck on “修复登录中”.
			if browserStoreErr != nil {
				writeError(encoder, req.ID, "browser_store_unavailable", browserStoreErr.Error())
				continue
			}
			var params struct {
				InstanceID string `json:"instance_id"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeError(encoder, req.ID, "invalid_params", "结束 Chrome 登录修复参数无效")
				continue
			}
			instance, err := browserStore.StopRepair(params.InstanceID)
			if err != nil {
				writeError(encoder, req.ID, "browser_repair_stop_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{
				Version: protocolVersion,
				ID:      req.ID,
				OK:      true,
				Result:  instance,
			})
		case "browser.close":
			if browserStoreErr != nil {
				writeError(encoder, req.ID, "browser_store_unavailable", browserStoreErr.Error())
				continue
			}
			var params struct {
				InstanceID string `json:"instance_id"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeError(encoder, req.ID, "invalid_params", "关闭浏览器实例参数无效")
				continue
			}
			instance, err := browserStore.Close(params.InstanceID)
			if err != nil {
				writeError(encoder, req.ID, "browser_close_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: instance})
		case "browser.native_credential":
			if accountStoreErr != nil {
				writeError(encoder, req.ID, "account_store_unavailable", accountStoreErr.Error())
				continue
			}
			if browserStoreErr != nil {
				writeError(encoder, req.ID, "browser_store_unavailable", browserStoreErr.Error())
				continue
			}
			var params struct {
				InstanceID string `json:"instance_id"`
				Secret     string `json:"secret"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeError(encoder, req.ID, "invalid_params", "原生浏览器凭据参数无效")
				continue
			}
			nativeSecret := strings.TrimSpace(os.Getenv("FUBAO_NATIVE_RPC_SECRET"))
			if nativeSecret == "" || params.Secret != nativeSecret {
				writeError(encoder, req.ID, "native_auth_failed", "原生浏览器凭据请求未授权")
				continue
			}
			accountID, err := browserStore.AccountID(params.InstanceID)
			if err != nil {
				writeError(encoder, req.ID, "browser_credential_failed", err.Error())
				continue
			}
			credential, err := accountStore.ParticipationCredential(accountID)
			if err != nil {
				writeError(encoder, req.ID, "browser_account_invalid", err.Error())
				continue
			}
			surface := string(browsers.SurfaceForAccountSource(credential.Source))
			_ = encoder.Encode(response{
				Version: protocolVersion,
				ID:      req.ID,
				OK:      true,
				Result: map[string]string{
					"instance_id":   params.InstanceID,
					"account_id":    credential.AccountID,
					"account_name":  credential.AccountName,
					"cookie":        credential.Cookie,
					"cookie_status": credential.CookieStatus,
					"surface":       surface,
				},
			})
		case "room.list":
			if roomStoreErr != nil {
				writeError(encoder, req.ID, "room_store_unavailable", roomStoreErr.Error())
				continue
			}
			_ = encoder.Encode(response{
				Version: protocolVersion,
				ID:      req.ID,
				OK:      true,
				Result:  roomStore.List(),
			})
		case "room.list_page":
			if roomStoreErr != nil {
				writeError(encoder, req.ID, "room_store_unavailable", roomStoreErr.Error())
				continue
			}
			var params struct {
				Offset int    `json:"offset"`
				Limit  int    `json:"limit"`
				Query  string `json:"query"`
				Source string `json:"source"`
			}
			if len(req.Params) > 0 {
				if err := json.Unmarshal(req.Params, &params); err != nil {
					writeError(encoder, req.ID, "invalid_params", "直播间分页参数无效")
					continue
				}
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: roomStore.PageBySource(params.Offset, params.Limit, params.Query, params.Source)})
		case "room.settings":
			if roomStoreErr != nil {
				writeError(encoder, req.ID, "room_store_unavailable", roomStoreErr.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: roomStore.Settings()})
		case "room.set_settings":
			if roomStoreErr != nil {
				writeError(encoder, req.ID, "room_store_unavailable", roomStoreErr.Error())
				continue
			}
			var params rooms.Settings
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeError(encoder, req.ID, "invalid_params", "直播间设置参数无效")
				continue
			}
			result, err := roomStore.SetSettings(params)
			if err != nil {
				writeError(encoder, req.ID, "room_settings_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: result})
		case "room.execute_cleanup":
			if roomStoreErr != nil {
				writeError(encoder, req.ID, "room_store_unavailable", roomStoreErr.Error())
				continue
			}
			var params struct {
				Cursor string `json:"cursor"`
				Limit  int    `json:"limit"`
			}
			if len(req.Params) > 0 {
				if err := json.Unmarshal(req.Params, &params); err != nil {
					writeError(encoder, req.ID, "invalid_params", "直播间自动清理参数无效")
					continue
				}
			}
			result, err := roomStore.ExecuteCleanupScoped(
				params.Cursor,
				params.Limit,
				time.Now(),
				permanentCenterRoomAccess(licenseManager),
			)
			if err != nil {
				writeError(encoder, req.ID, "room_cleanup_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: result})
		case "room.recycle_bin":
			if roomStoreErr != nil {
				writeError(encoder, req.ID, "room_store_unavailable", roomStoreErr.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: roomStore.RecycleBin()})
		case "room.center_exclusions":
			if roomStoreErr != nil {
				writeError(encoder, req.ID, "room_store_unavailable", roomStoreErr.Error())
				continue
			}
			if !permanentCenterRoomAccess(licenseManager) {
				_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: []rooms.CenterExclusion{}})
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: roomStore.CenterExclusions()})
		case "room.center_exclusion.restore":
			if roomStoreErr != nil {
				writeError(encoder, req.ID, "room_store_unavailable", roomStoreErr.Error())
				continue
			}
			var params struct {
				RoomID string `json:"room_id"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.RoomID) == "" {
				writeError(encoder, req.ID, "invalid_params", "解除中心库排除参数无效")
				continue
			}
			exclusion, exists := roomStore.CenterExclusion(params.RoomID)
			if !exists {
				writeError(encoder, req.ID, "room_center_exclusion_restore_failed", "中心库排除记录不存在")
				continue
			}
			if remoteSyncManagerErr != nil || remoteSyncManager == nil || remoteSyncPullScope(licenseManager) != remotesync.PullAll {
				writeError(encoder, req.ID, "room_center_exclusion_restore_failed", "只有已绑定中心库的永久专业版可以解除中心库排除")
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			err := remoteSyncManager.SyncCenterExclusions(ctx, roomStore)
			if err == nil {
				err = remoteSyncManager.RestoreCenterExclusion(ctx, exclusion.WebRID)
			}
			cancel()
			if err != nil {
				writeError(encoder, req.ID, "room_center_exclusion_restore_failed", err.Error())
				continue
			}
			result, err := roomStore.RestoreCenterExclusion(params.RoomID)
			if err != nil {
				writeError(encoder, req.ID, "room_center_exclusion_restore_failed", err.Error())
				continue
			}
			if redPacketStoreErr == nil {
				_ = redPacketStore.SyncRooms(roomStore.All())
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: result})
		case "room.recycle.restore":
			if roomStoreErr != nil {
				writeError(encoder, req.ID, "room_store_unavailable", roomStoreErr.Error())
				continue
			}
			var params struct {
				RoomID string `json:"room_id"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.RoomID) == "" {
				writeError(encoder, req.ID, "invalid_params", "恢复直播间参数无效")
				continue
			}
			result, err := roomStore.Restore(params.RoomID)
			if err != nil {
				writeError(encoder, req.ID, "room_restore_failed", err.Error())
				continue
			}
			if redPacketStoreErr == nil {
				_ = redPacketStore.SyncRooms(roomStore.All())
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: result})
		case "room.recycle.delete":
			if roomStoreErr != nil {
				writeError(encoder, req.ID, "room_store_unavailable", roomStoreErr.Error())
				continue
			}
			var params struct {
				RoomID string `json:"room_id"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.RoomID) == "" {
				writeError(encoder, req.ID, "invalid_params", "永久删除直播间参数无效")
				continue
			}
			allowCenterExclusion := permanentCenterRoomAccess(licenseManager)
			if err := roomStore.DeleteRecycledScoped(params.RoomID, allowCenterExclusion); err != nil {
				writeError(encoder, req.ID, "room_delete_failed", err.Error())
				continue
			}
			if allowCenterExclusion && remoteSyncManager != nil && remoteSyncManagerErr == nil && remoteSyncPullScope(licenseManager) == remotesync.PullAll {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				err := remoteSyncManager.SyncCenterExclusions(ctx, roomStore)
				cancel()
				if err != nil {
					writeError(encoder, req.ID, "room_center_exclusion_sync_failed", "已在本机排除，中心库同步失败并将在后台重试："+err.Error())
					continue
				}
			}
			if redPacketStoreErr == nil {
				_ = redPacketStore.SyncRooms(roomStore.All())
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: map[string]bool{"deleted": true}})
		case "room.migrate_legacy":
			if roomStoreErr != nil {
				writeError(encoder, req.ID, "room_store_unavailable", roomStoreErr.Error())
				continue
			}
			var params struct {
				LegacyDir string `json:"legacy_dir"`
			}
			if len(req.Params) > 0 {
				if err := json.Unmarshal(req.Params, &params); err != nil {
					writeError(encoder, req.ID, "invalid_params", "直播间迁移参数无效")
					continue
				}
			}
			result, err := roomStore.MigrateLegacy(params.LegacyDir)
			if err != nil {
				writeError(encoder, req.ID, "room_migration_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{
				Version: protocolVersion,
				ID:      req.ID,
				OK:      true,
				Result:  result,
			})
		case "room.import_ids":
			if roomStoreErr != nil {
				writeError(encoder, req.ID, "room_store_unavailable", roomStoreErr.Error())
				continue
			}
			var params struct {
				IDs     string `json:"ids"`
				Persist *bool  `json:"persist"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.IDs) == "" {
				writeError(encoder, req.ID, "invalid_params", "直播间导入内容为空")
				continue
			}
			persist := true
			if params.Persist != nil {
				persist = *params.Persist
			}
			result, err := roomStore.ImportIDsBatch(params.IDs, persist)
			if err != nil {
				writeError(encoder, req.ID, "room_import_failed", err.Error())
				continue
			}
			if persist && redPacketStoreErr == nil {
				_ = redPacketStore.SyncRooms(roomStore.All())
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: result})
		case "red_packet_monitor.list":
			if roomStoreErr != nil {
				writeError(encoder, req.ID, "room_store_unavailable", roomStoreErr.Error())
				continue
			}
			if redPacketStoreErr != nil {
				writeError(encoder, req.ID, "red_packet_store_unavailable", redPacketStoreErr.Error())
				continue
			}
			if err := redPacketStore.SyncRooms(roomStore.All()); err != nil {
				writeError(encoder, req.ID, "red_packet_store_unavailable", err.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: redPacketStore.List()})
		case "red_packet_monitor.list_page":
			if roomStoreErr != nil {
				writeError(encoder, req.ID, "room_store_unavailable", roomStoreErr.Error())
				continue
			}
			if redPacketStoreErr != nil {
				writeError(encoder, req.ID, "red_packet_store_unavailable", redPacketStoreErr.Error())
				continue
			}
			if redPacketStore.MonitorCount() != roomStore.CountAll() {
				if err := redPacketStore.SyncRooms(roomStore.All()); err != nil {
					writeError(encoder, req.ID, "red_packet_store_unavailable", err.Error())
					continue
				}
			}
			var params struct {
				RoomIDs []string `json:"room_ids"`
			}
			if len(req.Params) > 0 {
				if err := json.Unmarshal(req.Params, &params); err != nil {
					writeError(encoder, req.ID, "invalid_params", "红包监测分页参数无效")
					continue
				}
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: redPacketStore.PageForRooms(params.RoomIDs)})
		case "red_packet_monitor.settings":
			if redPacketStoreErr != nil {
				writeError(encoder, req.ID, "red_packet_store_unavailable", redPacketStoreErr.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: redPacketStore.GetMonitoringSettings()})
		case "red_packet_monitor.set_settings":
			if redPacketStoreErr != nil {
				writeError(encoder, req.ID, "red_packet_store_unavailable", redPacketStoreErr.Error())
				continue
			}
			var params redpacket.MonitoringSettings
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeError(encoder, req.ID, "invalid_params", "监测设置参数无效")
				continue
			}
			result, err := redPacketStore.SetMonitoringSettings(params)
			if err != nil {
				writeError(encoder, req.ID, "monitor_settings_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: result})
		case "red_packet_monitor.events":
			if redPacketStoreErr != nil {
				writeError(encoder, req.ID, "red_packet_store_unavailable", redPacketStoreErr.Error())
				continue
			}
			var params struct {
				MonitorID string `json:"monitor_id"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.MonitorID) == "" {
				writeError(encoder, req.ID, "invalid_params", "红包监测事件参数无效")
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: redPacketStore.Events(params.MonitorID)})
		case "red_packet_event.list":
			if redPacketStoreErr != nil {
				writeError(encoder, req.ID, "red_packet_store_unavailable", redPacketStoreErr.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: redPacketStore.EventsAll()})
		case "red_packet_participation.list":
			if redPacketStoreErr != nil {
				writeError(encoder, req.ID, "red_packet_store_unavailable", redPacketStoreErr.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: redPacketStore.ParticipationRecords()})
		case "red_packet_participation.logs":
			if redPacketStoreErr != nil {
				writeError(encoder, req.ID, "red_packet_store_unavailable", redPacketStoreErr.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: redPacketStore.ParticipationTraces()})
		case "red_packet_participation.clear_logs":
			if redPacketStoreErr != nil {
				writeError(encoder, req.ID, "red_packet_store_unavailable", redPacketStoreErr.Error())
				continue
			}
			if err := redPacketStore.ClearParticipationTraces(); err != nil {
				writeError(encoder, req.ID, "participation_log_clear_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: map[string]any{"cleared": true}})
		case "red_packet_participation.settings":
			if redPacketStoreErr != nil {
				writeError(encoder, req.ID, "red_packet_store_unavailable", redPacketStoreErr.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: redPacketStore.GetParticipationSettings()})
		case "red_packet_participation.set_settings":
			if redPacketStoreErr != nil {
				writeError(encoder, req.ID, "red_packet_store_unavailable", redPacketStoreErr.Error())
				continue
			}
			var params redpacket.ParticipationSettings
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeError(encoder, req.ID, "invalid_params", "红包参与设置参数无效")
				continue
			}
			// Preserve newer persisted values when an older frontend omits fields it
			// does not know yet. Explicit zero remains valid for the countdown and
			// result delay; max attempts is normalized to a safe positive default.
			var rawParams map[string]json.RawMessage
			if err := json.Unmarshal(req.Params, &rawParams); err == nil {
				if _, present := rawParams["participation_countdown_seconds"]; !present {
					params.ParticipationCountdownSeconds = redPacketStore.GetParticipationSettings().ParticipationCountdownSeconds
				}
				stored := redPacketStore.GetParticipationSettings()
				if _, present := rawParams["draw_result_delay_seconds"]; !present {
					params.DrawResultDelaySeconds = stored.DrawResultDelaySeconds
				}
				if _, present := rawParams["draw_result_max_attempts"]; !present {
					params.DrawResultMaxAttempts = stored.DrawResultMaxAttempts
				}
			}
			result, err := redPacketStore.SetParticipationSettings(params)
			if err != nil {
				writeError(encoder, req.ID, "red_packet_settings_failed", err.Error())
				continue
			}
			if pageParticipation != nil && browserStoreErr == nil {
				for _, instance := range browserStore.List() {
					state := redPacketStore.GetParticipationState(instance.AccountID, time.Now())
					if state.Stopped && len(redPacketStore.PendingDraws(instance.AccountID)) == 0 {
						_ = redPacketStore.FinishParticipationTask(instance.AccountID, state.StopReason)
						pageParticipation.StopAccount(instance.AccountID)
					}
				}
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: result})
		case "red_packet_participation.schedule.list":
			if redPacketStoreErr != nil {
				writeError(encoder, req.ID, "red_packet_store_unavailable", redPacketStoreErr.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: redPacketStore.ParticipationSchedules()})
		case "red_packet_participation.schedule.create":
			if redPacketStoreErr != nil {
				writeError(encoder, req.ID, "red_packet_store_unavailable", redPacketStoreErr.Error())
				continue
			}
			var params redpacket.ParticipationSchedule
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeError(encoder, req.ID, "invalid_params", "红包参与计划参数无效")
				continue
			}
			result, err := redPacketStore.CreateParticipationSchedule(params, time.Now())
			if err != nil {
				writeError(encoder, req.ID, "participation_schedule_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: result})
		case "red_packet_participation.schedule.delete":
			if redPacketStoreErr != nil {
				writeError(encoder, req.ID, "red_packet_store_unavailable", redPacketStoreErr.Error())
				continue
			}
			var params struct {
				ScheduleID string `json:"schedule_id"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.ScheduleID) == "" {
				writeError(encoder, req.ID, "invalid_params", "红包参与计划参数无效")
				continue
			}
			if err := redPacketStore.DeleteParticipationSchedule(params.ScheduleID); err != nil {
				writeError(encoder, req.ID, "participation_schedule_delete_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: map[string]any{"deleted": true}})
		case "red_packet_participation.schedule.claim_due":
			if redPacketStoreErr != nil {
				writeError(encoder, req.ID, "red_packet_store_unavailable", redPacketStoreErr.Error())
				continue
			}
			result, err := redPacketStore.ClaimDueParticipationSchedules(time.Now())
			if err != nil {
				writeError(encoder, req.ID, "participation_schedule_claim_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: result})
		case "red_packet_participation.batch_result":
			if redPacketStoreErr != nil {
				writeError(encoder, req.ID, "red_packet_store_unavailable", redPacketStoreErr.Error())
				continue
			}
			var params struct {
				ScheduleID string   `json:"schedule_id"`
				Mode       string   `json:"mode"`
				Started    int      `json:"started"`
				Skipped    int      `json:"skipped"`
				AccountIDs []string `json:"account_ids"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || params.Started < 0 || params.Skipped < 0 {
				writeError(encoder, req.ID, "invalid_params", "红包参与批量执行结果无效")
				continue
			}
			if err := redPacketStore.RecordParticipationBatchResult(params.ScheduleID, params.Mode, params.Started, params.Skipped, params.AccountIDs); err != nil {
				writeError(encoder, req.ID, "participation_batch_result_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: map[string]any{"recorded": true}})
		case "red_packet_participation.contexts":
			if redPacketStoreErr != nil || browserStoreErr != nil || pageParticipation == nil {
				writeError(encoder, req.ID, "participation_unavailable", "红包参与状态不可用")
				continue
			}
			prepared := map[string]bool{}
			for _, context := range pageParticipation.Contexts() {
				prepared[context.InstanceID] = true
			}
			eligibleAccounts := map[string]bool{}
			if accountStoreErr == nil {
				for _, credential := range accountStore.RedPacketParticipationCredentials(time.Now()) {
					eligibleAccounts[credential.AccountID] = true
				}
			}
			states := make([]map[string]any, 0)
			for _, instance := range browserStore.List() {
				state := redPacketStore.GetParticipationState(instance.AccountID, time.Now())
				pendingDraws := redPacketStore.PendingDraws(instance.AccountID)
				pendingWebRID := ""
				if len(pendingDraws) > 0 {
					pendingWebRID = pendingDraws[0].WebRID
				}
				hasNativeContext := prepared[instance.ID]
				resumable := !hasNativeContext && state.Active && len(pendingDraws) > 0
				states = append(states, map[string]any{
					"instance_id": instance.ID, "account_id": instance.AccountID,
					"prepared":  hasNativeContext,
					"accepting": hasNativeContext && eligibleAccounts[instance.AccountID] && state.Active && !state.Stopped && !state.WaitingDraw,
					"active":    hasNativeContext && state.Active, "task_active": state.Active, "resumable": resumable,
					"task_id": state.TaskID,
					"stopped": state.Stopped, "stop_reason": state.StopReason,
					"waiting_draw": state.WaitingDraw, "waiting_reason": state.WaitingReason,
					"pending_draw_count": len(pendingDraws), "pending_result_web_rid": pendingWebRID,
					"cooldown_until": state.CooldownUntil,
					"join_count":     state.JoinCount, "win_count": state.WinCount,
				})
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: states})
		case "activity.list":
			if redPacketStoreErr != nil {
				writeError(encoder, req.ID, "red_packet_store_unavailable", redPacketStoreErr.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: redPacketStore.Activities()})
		case "red_packet_participation.overview":
			if redPacketStoreErr != nil {
				writeError(encoder, req.ID, "red_packet_store_unavailable", redPacketStoreErr.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: redPacketStore.ParticipationOverview()})
		case "activity.stop_participation_batch":
			if redPacketStoreErr != nil {
				writeError(encoder, req.ID, "red_packet_store_unavailable", redPacketStoreErr.Error())
				continue
			}
			var params struct {
				ActivityID string `json:"activity_id"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.ActivityID) == "" {
				writeError(encoder, req.ID, "invalid_params", "红包参与批次参数无效")
				continue
			}
			accountIDs, err := redPacketStore.StopParticipationBatch(params.ActivityID)
			if err != nil {
				writeError(encoder, req.ID, "participation_batch_stop_failed", err.Error())
				continue
			}
			if pageParticipation != nil {
				for _, accountID := range accountIDs {
					pageParticipation.StopAccount(accountID)
				}
			}
			if accountStoreErr == nil {
				for _, accountID := range accountIDs {
					_, _ = accountStore.SetRedPacketAPIEnabled(accountID, false)
				}
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: map[string]any{"account_ids": accountIDs}})
		case "red_packet_participation.native_context":
			var params struct {
				InstanceID             string `json:"instance_id"`
				Ready                  bool   `json:"ready"`
				ResultOnly             bool   `json:"result_only"`
				AllowChallengeRecovery bool   `json:"allow_challenge_recovery"`
				Secret                 string `json:"secret"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.InstanceID) == "" {
				writeError(encoder, req.ID, "invalid_params", "浏览器红包参与上下文参数无效")
				continue
			}
			if strings.TrimSpace(params.Secret) == "" || params.Secret != strings.TrimSpace(os.Getenv("FUBAO_NATIVE_RPC_SECRET")) {
				writeError(encoder, req.ID, "forbidden", "原生请求认证失败")
				continue
			}
			if pageParticipation == nil || redPacketParticipant == nil {
				writeError(encoder, req.ID, "participation_unavailable", "红包页面参与器不可用")
				continue
			}
			accountID, err := browserStore.AccountID(params.InstanceID)
			if err != nil {
				writeError(encoder, req.ID, "participation_context_failed", err.Error())
				continue
			}
			accountName := "参与账号"
			challengeBlocked := false
			if accountStoreErr == nil {
				for _, account := range accountStore.List(accounts.RoleParticipation) {
					if account.ID == accountID {
						if params.Ready && !params.ResultOnly && !params.AllowChallengeRecovery &&
							account.Participation != nil && account.Participation.LastRedPacketStatus == "challenge_blocked" {
							challengeBlocked = true
						}
						accountName = strings.TrimSpace(account.Nickname)
						if accountName == "" {
							accountName = strings.TrimSpace(account.Name)
						}
						break
					}
				}
			}
			if challengeBlocked {
				writeError(encoder, req.ID, "participation_challenge_blocked", "该账号当前处于验证拦截，请先处理验证后手动重新启动参与")
				continue
			}
			if params.Ready && !params.ResultOnly {
				if err := redPacketStore.RecordParticipationStarted(accountID, accountName); err != nil {
					writeError(encoder, req.ID, "participation_task_start_failed", err.Error())
					continue
				}
			}
			accountID, err = pageParticipation.SetContext(params.InstanceID, params.Ready)
			if err != nil {
				if params.Ready && !params.ResultOnly {
					_ = redPacketStore.FinishParticipationTask(accountID, "启动失败")
				}
				writeError(encoder, req.ID, "participation_context_failed", err.Error())
				continue
			}
			if accountStoreErr == nil && !params.ResultOnly {
				if _, setErr := accountStore.SetRedPacketAPIEnabled(accountID, params.Ready); setErr != nil {
					if params.Ready {
						pageParticipation.StopAccount(accountID)
						_ = redPacketStore.FinishParticipationTask(accountID, "启动失败")
					} else {
						_ = redPacketStore.FinishParticipationTask(accountID, "手动停止")
					}
					writeError(encoder, req.ID, "participation_account_toggle_failed", setErr.Error())
					continue
				}
			}
			if !params.Ready {
				_ = redPacketStore.FinishParticipationTask(accountID, "手动停止")
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: map[string]any{
				"account_id": accountID, "instance_id": params.InstanceID, "ready": params.Ready,
			}})
			if params.Ready && redPacketStore != nil {
				// Explicit preparation should immediately retry current, unexpired
				// events that previously failed outside a page context.
				if !params.ResultOnly {
					for _, item := range redPacketStore.EventsAll() {
						expiresAt, parseErr := time.Parse(time.RFC3339Nano, item.ExpiresAt)
						if parseErr == nil && time.Now().Before(expiresAt) {
							redPacketParticipant.RetryEventForAccount(item, accountID)
						}
					}
				}
				redPacketParticipant.ResolvePendingDraws(accountID)
			}
		case "red_packet_participation.native_next":
			var params struct {
				Secret string `json:"secret"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.Secret) == "" || params.Secret != strings.TrimSpace(os.Getenv("FUBAO_NATIVE_RPC_SECRET")) {
				writeError(encoder, req.ID, "forbidden", "原生请求认证失败")
				continue
			}
			if pageParticipation == nil {
				_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: nil})
				continue
			}
			task, ok := pageParticipation.Next()
			if !ok {
				_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: nil})
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: task})
		case "red_packet_participation.native_complete":
			var params struct {
				TaskID           string `json:"task_id"`
				Endpoint         string `json:"endpoint"`
				HTTPStatus       int    `json:"http_status"`
				Body             string `json:"body"`
				Error            string `json:"error"`
				Attempts         int    `json:"attempts"`
				ContextMissing   bool   `json:"context_missing"`
				LoginExpired     bool   `json:"login_expired"`
				ChallengeBlocked bool   `json:"challenge_blocked"`
				Secret           string `json:"secret"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.TaskID) == "" {
				writeError(encoder, req.ID, "invalid_params", "原生红包参与结果参数无效")
				continue
			}
			if strings.TrimSpace(params.Secret) == "" || params.Secret != strings.TrimSpace(os.Getenv("FUBAO_NATIVE_RPC_SECRET")) {
				writeError(encoder, req.ID, "forbidden", "原生请求认证失败")
				continue
			}
			if pageParticipation == nil || !pageParticipation.Complete(params.TaskID, redpacket.PageParticipationResponse{
				Endpoint: params.Endpoint, HTTPStatus: params.HTTPStatus, Body: params.Body,
				Error: params.Error, Attempts: params.Attempts,
				ContextMissing: params.ContextMissing, LoginExpired: params.LoginExpired,
				ChallengeBlocked: params.ChallengeBlocked,
			}) {
				writeError(encoder, req.ID, "participation_task_missing", "原生红包参与任务已结束或不存在")
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: map[string]any{"completed": true}})
		case "red_packet_monitor.start_all":
			if accountStoreErr != nil {
				writeError(encoder, req.ID, "account_store_unavailable", accountStoreErr.Error())
				continue
			}
			if roomStoreErr != nil || redPacketStoreErr != nil {
				writeError(encoder, req.ID, "red_packet_store_unavailable", "红包监测存储不可用")
				continue
			}
			result, err := startAllEligibleRoomMonitors(accountStore, roomStore, redPacketStore)
			if err != nil {
				code := "red_packet_start_failed"
				if len(monitoringPoolCredentials(accountStore)) == 0 {
					code = "monitor_account_missing"
				}
				writeError(encoder, req.ID, code, err.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: result})
		case "red_packet_monitor.stop_all":
			if redPacketStoreErr != nil {
				writeError(encoder, req.ID, "red_packet_store_unavailable", redPacketStoreErr.Error())
				continue
			}
			stopped, err := redPacketStore.StopAll()
			if err != nil {
				writeError(encoder, req.ID, "red_packet_stop_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: map[string]int{"stopped": stopped}})
		case "red_packet_monitor.start":
			if accountStoreErr != nil {
				writeError(encoder, req.ID, "account_store_unavailable", accountStoreErr.Error())
				continue
			}
			if redPacketStoreErr != nil {
				writeError(encoder, req.ID, "red_packet_store_unavailable", redPacketStoreErr.Error())
				continue
			}
			var params struct {
				MonitorID string `json:"monitor_id"`
				AccountID string `json:"account_id"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.MonitorID) == "" {
				writeError(encoder, req.ID, "invalid_params", "启动红包监测参数无效")
				continue
			}
			if strings.TrimSpace(params.AccountID) == "" {
				credentials := monitoringPoolCredentials(accountStore)
				if len(credentials) == 0 {
					writeError(encoder, req.ID, "monitor_account_missing", "请先导入或添加监测账号")
					continue
				}
				if err := redPacketStore.StartPooled(params.MonitorID, credentials); err != nil {
					writeError(encoder, req.ID, "red_packet_start_failed", err.Error())
					continue
				}
			} else {
				credential, err := accountStore.MonitoringCredential(params.AccountID)
				if err != nil {
					writeError(encoder, req.ID, "monitor_account_invalid", err.Error())
					continue
				}
				if err := redPacketStore.Start(params.MonitorID, credential.AccountID, credential.AccountName, credential.Cookie); err != nil {
					writeError(encoder, req.ID, "red_packet_start_failed", err.Error())
					continue
				}
			}
			monitor, ok := redPacketStore.Get(params.MonitorID)
			if !ok {
				writeError(encoder, req.ID, "red_packet_store_unavailable", "红包监测状态读取失败")
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: map[string]any{"started": true, "monitor": monitor}})
		case "red_packet_monitor.stop":
			if redPacketStoreErr != nil {
				writeError(encoder, req.ID, "red_packet_store_unavailable", redPacketStoreErr.Error())
				continue
			}
			var params struct {
				MonitorID string `json:"monitor_id"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.MonitorID) == "" {
				writeError(encoder, req.ID, "invalid_params", "停止红包监测参数无效")
				continue
			}
			if err := redPacketStore.Stop(params.MonitorID); err != nil {
				writeError(encoder, req.ID, "red_packet_stop_failed", err.Error())
				continue
			}
			monitor, ok := redPacketStore.Get(params.MonitorID)
			if !ok {
				writeError(encoder, req.ID, "red_packet_store_unavailable", "红包监测状态读取失败")
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: map[string]any{"stopped": true, "monitor": monitor}})
		case "red_packet_monitor.delete":
			if redPacketStoreErr != nil {
				writeError(encoder, req.ID, "red_packet_store_unavailable", redPacketStoreErr.Error())
				continue
			}
			var params struct {
				MonitorID string `json:"monitor_id"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.MonitorID) == "" {
				writeError(encoder, req.ID, "invalid_params", "删除红包监测参数无效")
				continue
			}
			if err := redPacketStore.Delete(params.MonitorID); err != nil {
				writeError(encoder, req.ID, "red_packet_delete_failed", err.Error())
				continue
			}
			_ = encoder.Encode(response{Version: protocolVersion, ID: req.ID, OK: true, Result: map[string]bool{"deleted": true}})
		default:
			_ = encoder.Encode(response{
				Version: protocolVersion,
				ID:      req.ID,
				OK:      false,
				Error: &rpcError{
					Code:    "method_not_found",
					Message: fmt.Sprintf("尚未实现方法：%s", req.Method),
				},
			})
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "ipc scanner stopped: %v\n", err)
	}
}

func writeError(encoder *json.Encoder, id, code, message string) {
	_ = encoder.Encode(response{
		Version: protocolVersion,
		ID:      id,
		OK:      false,
		Error:   &rpcError{Code: code, Message: message},
	})
}

// browserInstancesWithSurfaces refreshes each instance host mode from the
// current account provenance so imported accounts stay on external Chrome
// until a native rebind/QR login promotes them to the embedded surface.
func browserInstancesWithSurfaces(browserStore *browsers.Store, accountStore *accounts.Store, accountStoreErr error) []browsers.Instance {
	if browserStore == nil {
		return nil
	}
	if accountStore == nil || accountStoreErr != nil {
		return browserStore.List()
	}
	sources := map[string]string{}
	for _, account := range accountStore.List(accounts.RoleParticipation) {
		sources[account.ID] = account.Source
	}
	return browserStore.ApplyAccountSources(sources)
}

func monitoringPoolCredentials(store *accounts.Store) []redpacket.AccountCredential {
	views := store.List(accounts.RoleMonitoring)
	credentials := make([]redpacket.AccountCredential, 0, len(views))
	for _, view := range views {
		credential, err := store.MonitoringCredential(view.ID)
		if err != nil {
			continue
		}
		credentials = append(credentials, redpacket.AccountCredential{
			AccountID:   credential.AccountID,
			AccountName: credential.AccountName,
			Cookie:      credential.Cookie,
		})
	}
	return credentials
}

func refreshRunningMonitoringPool(store *accounts.Store, redPacketStore *redpacket.Store) redpacket.PoolRefreshResult {
	if store == nil || redPacketStore == nil {
		return redpacket.PoolRefreshResult{}
	}
	return redPacketStore.RefreshMonitoringPool(monitoringPoolCredentials(store))
}

func startAllEligibleRoomMonitors(accountStore *accounts.Store, roomStore *rooms.Store, redPacketStore *redpacket.Store) (redpacket.PoolStartResult, error) {
	if accountStore == nil || roomStore == nil || redPacketStore == nil {
		return redpacket.PoolStartResult{}, errors.New("红包监测存储不可用")
	}
	if err := redPacketStore.SyncRooms(roomStore.All()); err != nil {
		return redpacket.PoolStartResult{}, err
	}
	credentials := monitoringPoolCredentials(accountStore)
	if len(credentials) == 0 {
		return redpacket.PoolStartResult{}, errors.New("请先导入或添加可用的监测账号")
	}
	return redPacketStore.StartAllPool(credentials)
}

// runParticipationMonitorPrewarm is intentionally native and page-agnostic.
// Scheduled monitoring preparation therefore continues while the user is on
// another page, the main window is hidden in the tray, or no browser card is
// mounted. Failed checks stay unmarked and are retried until the run is due.
func runParticipationMonitorPrewarm(ctx context.Context, accountStore *accounts.Store, roomStore *rooms.Store, redPacketStore *redpacket.Store) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		settings := roomStore.Settings()
		advance := time.Duration(settings.ParticipationPrewarmMinutes) * time.Minute
		pending := redPacketStore.PendingParticipationMonitorPrewarms(time.Now(), advance)
		if len(pending) > 0 {
			if result, err := startAllEligibleRoomMonitors(accountStore, roomStore, redPacketStore); err == nil {
				for _, execution := range pending {
					_ = redPacketStore.RecordParticipationMonitorPrewarm(execution, advance, result.Started, result.AccountCount)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func mergeFollowingLiveResult(roomStore *rooms.Store, redPacketStore *redpacket.Store, credential accounts.BrowserCredential, result followinglive.Result) error {
	if roomStore == nil {
		return errors.New("直播间存储不可用")
	}
	items := make([]rooms.FollowingLiveRoom, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, rooms.FollowingLiveRoom{
			RoomID:       item.RoomID,
			WebRID:       item.WebRID,
			Title:        item.Title,
			StreamerName: item.Nickname,
		})
	}
	if _, err := roomStore.SyncFollowingLive(credential.AccountID, credential.AccountName, items, result.RefreshedAt); err != nil {
		return err
	}
	if redPacketStore != nil {
		return redPacketStore.SyncRooms(roomStore.All())
	}
	return nil
}

// runFollowingLiveSync keeps discovery account-centric: one request per
// canonical participation account, regardless of how often instance cards
// rerender. The first pass runs shortly after startup, then refreshes once per
// minute. Temporary network failures keep the previous canonical room data.
func runFollowingLiveSync(ctx context.Context, accountStore *accounts.Store, browserStore *browsers.Store, service *followinglive.Service, roomStore *rooms.Store, redPacketStore *redpacket.Store) {
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	for {
		seenAccounts := map[string]struct{}{}
		for _, instance := range browserStore.List() {
			accountID := strings.TrimSpace(instance.AccountID)
			if accountID == "" {
				continue
			}
			if _, exists := seenAccounts[accountID]; exists {
				continue
			}
			seenAccounts[accountID] = struct{}{}
			credential, err := accountStore.ParticipationCredential(accountID)
			if err != nil {
				continue
			}
			requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			result, err := service.Fetch(requestCtx, credential.AccountID, credential.Cookie, false)
			cancel()
			if err == nil && !result.Stale {
				_ = mergeFollowingLiveResult(roomStore, redPacketStore, credential, result)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
		}

		timer.Reset(60 * time.Second)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

func runRemoteSyncSnapshots(ctx context.Context, manager *remotesync.Manager, roomStore *rooms.Store, redPacketStore *redpacket.Store, licenseManager *license.Manager) {
	if manager == nil || roomStore == nil || redPacketStore == nil {
		return
	}
	syncSnapshot := func() {
		_ = manager.SyncSnapshot(roomStore.List(), redPacketStore.List(), redPacketStore.EventsAll())
		pullCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		_ = manager.PullOnceScoped(pullCtx, roomStore, redPacketStore, remoteSyncPullScope(licenseManager))
		cancel()
	}
	syncSnapshot()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			syncSnapshot()
		}
	}
}

func remoteSyncPullScope(manager *license.Manager) remotesync.PullScope {
	if manager == nil {
		return remotesync.PullNone
	}
	status := manager.Status()
	if status.State != "active" {
		return remotesync.PullNone
	}
	if strings.TrimSpace(status.ExpiresAt) == "" {
		return remotesync.PullAll
	}
	return remotesync.PullRedPackets
}

func permanentCenterRoomAccess(manager *license.Manager) bool {
	return remoteSyncPullScope(manager) == remotesync.PullAll
}

func browserInstanceCreateLimit(manager *license.Manager) int {
	if manager != nil && manager.Status().State == "active" {
		return 0
	}
	return 1
}
