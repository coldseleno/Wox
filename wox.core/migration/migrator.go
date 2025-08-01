package migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"
	"time"
	"wox/common"
	"wox/database"
	"wox/i18n"
	"wox/setting"
	"wox/util"
	"wox/util/locale"

	_ "github.com/mattn/go-sqlite3"
	"gorm.io/gorm"
)

// This file contains the logic for a one-time migration from the old JSON-based settings
// to the new SQLite database. It is designed to be self-contained.

// oldPlatformSettingValue mirrors the old PlatformSettingValue[T] generic struct.
// We define it locally to avoid dependencies on the old setting structure.
type oldPlatformSettingValue[T any] struct {
	WinValue   T `json:"WinValue"`
	MacValue   T `json:"MacValue"`
	LinuxValue T `json:"LinuxValue"`
}

func (p *oldPlatformSettingValue[T]) Get() T {
	// This is a simplified Get method for migration purposes.
	// It doesn't represent the full platform-specific logic of the original.
	// It is kept here for reference but should not be used for migration.
	// The entire object should be marshalled to JSON instead.
	if util.IsMacOS() {
		return p.MacValue
	}
	if util.IsWindows() {
		return p.WinValue
	}
	if util.IsLinux() {
		return p.LinuxValue
	}

	// Default to Mac value as a fallback
	return p.MacValue
}

// oldWoxSetting is a snapshot of the old WoxSetting struct.
type oldWoxSetting struct {
	EnableAutostart      oldPlatformSettingValue[bool]
	MainHotkey           oldPlatformSettingValue[string]
	SelectionHotkey      oldPlatformSettingValue[string]
	UsePinYin            bool
	SwitchInputMethodABC bool
	HideOnStart          bool
	HideOnLostFocus      bool
	ShowTray             bool
	LangCode             i18n.LangCode
	QueryHotkeys         oldPlatformSettingValue[[]oldQueryHotkey]
	QueryShortcuts       []oldQueryShortcut
	LastQueryMode        string
	ShowPosition         string
	AIProviders          []oldAIProvider
	EnableAutoBackup     bool
	EnableAutoUpdate     bool
	CustomPythonPath     oldPlatformSettingValue[string]
	CustomNodejsPath     oldPlatformSettingValue[string]
	HttpProxyEnabled     oldPlatformSettingValue[bool]
	HttpProxyUrl         oldPlatformSettingValue[string]
	AppWidth             int
	MaxResultCount       int
	ThemeId              string
	LastWindowX          int
	LastWindowY          int
}

// oldQueryHotkey is a snapshot of the old QueryHotkey struct.
type oldQueryHotkey struct {
	Hotkey            string
	Query             string
	IsSilentExecution bool
}

// oldQueryShortcut is a snapshot of the old QueryShortcut struct.
type oldQueryShortcut struct {
	Shortcut string
	Query    string
}

// oldAIProvider is a snapshot of the old AIProvider struct.
type oldAIProvider struct {
	Name   common.ProviderName
	ApiKey string
	Host   string
}

// oldQueryHistory is a snapshot of the old QueryHistory struct.
type oldQueryHistory struct {
	Query     common.PlainQuery
	Timestamp int64
}

// oldWoxAppData is a snapshot of the old WoxAppData struct.
type oldWoxAppData struct {
	QueryHistories  []oldQueryHistory
	FavoriteResults *util.HashMap[string, bool]
}

func getOldDefaultWoxSetting() oldWoxSetting {
	usePinYin := false
	langCode := i18n.LangCodeEnUs
	switchInputMethodABC := false
	if locale.IsZhCN() {
		usePinYin = true
		switchInputMethodABC = true
		langCode = i18n.LangCodeZhCn
	}

	return oldWoxSetting{
		MainHotkey:           oldPlatformSettingValue[string]{WinValue: "alt+space", MacValue: "command+space", LinuxValue: "ctrl+ctrl"},
		SelectionHotkey:      oldPlatformSettingValue[string]{WinValue: "win+alt+space", MacValue: "command+option+space", LinuxValue: "ctrl+shift+j"},
		UsePinYin:            usePinYin,
		SwitchInputMethodABC: switchInputMethodABC,
		ShowTray:             true,
		HideOnLostFocus:      true,
		LangCode:             langCode,
		LastQueryMode:        "empty",
		ShowPosition:         "mouse_screen",
		AppWidth:             800,
		MaxResultCount:       10,
		ThemeId:              "e4006bd3-6bfe-4020-8d1c-4c32a8e567e5",
		EnableAutostart:      oldPlatformSettingValue[bool]{WinValue: false, MacValue: false, LinuxValue: false},
		HttpProxyEnabled:     oldPlatformSettingValue[bool]{WinValue: false, MacValue: false, LinuxValue: false},
		HttpProxyUrl:         oldPlatformSettingValue[string]{WinValue: "", MacValue: "", LinuxValue: ""},
		CustomPythonPath:     oldPlatformSettingValue[string]{WinValue: "", MacValue: "", LinuxValue: ""},
		CustomNodejsPath:     oldPlatformSettingValue[string]{WinValue: "", MacValue: "", LinuxValue: ""},
		EnableAutoBackup:     true,
		EnableAutoUpdate:     true,
		LastWindowX:          -1,
		LastWindowY:          -1,
	}
}

func Run(ctx context.Context) error {
	// if database exists, no need to migrate
	if _, err := os.Stat(util.GetLocation().GetUserDataDirectory() + "wox.db"); err == nil {
		util.GetLogger().Info(ctx, "database found, skip for migrate.")
		return nil
	}

	util.GetLogger().Info(ctx, "database not found. Checking for old configuration files to migrate.")

	oldSettingPath := util.GetLocation().GetWoxSettingPath()
	oldAppDataPath := util.GetLocation().GetWoxAppDataPath()

	_, settingStatErr := os.Stat(oldSettingPath)
	_, appDataStatErr := os.Stat(oldAppDataPath)

	if os.IsNotExist(settingStatErr) && os.IsNotExist(appDataStatErr) {
		util.GetLogger().Info(ctx, "no old configuration files found. Skipping migration.")
		return nil
	}

	util.GetLogger().Info(ctx, "old configuration files found. Starting migration process.")

	migrateDB := database.GetDB()

	// Load old settings
	oldSettings := getOldDefaultWoxSetting()
	if _, err := os.Stat(oldSettingPath); err == nil {
		fileContent, readErr := os.ReadFile(oldSettingPath)
		if readErr == nil && len(fileContent) > 0 {
			if unmarshalErr := json.Unmarshal(fileContent, &oldSettings); unmarshalErr != nil {
				util.GetLogger().Warn(ctx, fmt.Sprintf("failed to unmarshal old wox.setting.json: %v, will use defaults for migration.", unmarshalErr))
			} else {
				util.GetLogger().Info(ctx, "successfully loaded old wox.setting.json for migration.")
			}
		}
	}

	// Load old app data
	var oldAppData oldWoxAppData
	if oldAppData.QueryHistories == nil {
		oldAppData.QueryHistories = []oldQueryHistory{}
	}
	if _, err := os.Stat(oldAppDataPath); err == nil {
		fileContent, readErr := os.ReadFile(oldAppDataPath)
		if readErr == nil && len(fileContent) > 0 {
			if json.Unmarshal(fileContent, &oldAppData) != nil {
				util.GetLogger().Warn(ctx, "failed to unmarshal old wox.app.data.json, will use defaults for migration.")
			}
		}
	}

	tx := migrateDB.Begin()
	if tx.Error != nil {
		return tx.Error
	}

	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
			panic(r)
		} else if err := tx.Error; err != nil {
			tx.Rollback()
		}
	}()

	woxSettingStore := setting.NewWoxSettingStore(tx)

	// Migrate simple settings
	settingsToMigrate := map[string]interface{}{
		"EnableAutostart":      oldSettings.EnableAutostart,
		"MainHotkey":           oldSettings.MainHotkey,
		"SelectionHotkey":      oldSettings.SelectionHotkey,
		"UsePinYin":            oldSettings.UsePinYin,
		"SwitchInputMethodABC": oldSettings.SwitchInputMethodABC,
		"HideOnStart":          oldSettings.HideOnStart,
		"HideOnLostFocus":      oldSettings.HideOnLostFocus,
		"ShowTray":             oldSettings.ShowTray,
		"LangCode":             oldSettings.LangCode,
		"QueryMode":            oldSettings.LastQueryMode, // Migrate LastQueryMode to QueryMode
		"ShowPosition":         oldSettings.ShowPosition,
		"EnableAutoBackup":     oldSettings.EnableAutoBackup,
		"EnableAutoUpdate":     oldSettings.EnableAutoUpdate,
		"CustomPythonPath":     oldSettings.CustomPythonPath,
		"CustomNodejsPath":     oldSettings.CustomNodejsPath,
		"HttpProxyEnabled":     oldSettings.HttpProxyEnabled,
		"HttpProxyUrl":         oldSettings.HttpProxyUrl,
		"AppWidth":             oldSettings.AppWidth,
		"MaxResultCount":       oldSettings.MaxResultCount,
		"ThemeId":              oldSettings.ThemeId,
		"LastWindowX":          oldSettings.LastWindowX,
		"LastWindowY":          oldSettings.LastWindowY,

		"QueryHotkeys":   oldSettings.QueryHotkeys,
		"QueryShortcuts": oldSettings.QueryShortcuts,
		"AIProviders":    oldSettings.AIProviders,
	}

	util.GetLogger().Info(ctx, fmt.Sprintf("migrating %d core settings", len(settingsToMigrate)))
	for key, value := range settingsToMigrate {
		util.GetLogger().Info(ctx, fmt.Sprintf("migrating setting %s", key))
		if err := woxSettingStore.Set(key, value); err != nil {
			return fmt.Errorf("failed to migrate setting %s: %w", key, err)
		}
	}

	// Migrate plugin settings
	pluginDir := util.GetLocation().GetPluginSettingDirectory()
	dirs, err := os.ReadDir(pluginDir)
	if err == nil {
		for _, file := range dirs {
			if file.IsDir() {
				continue
			}
			if !strings.HasSuffix(file.Name(), ".json") {
				continue
			}
			if strings.Contains(file.Name(), "wox") {
				continue
			}

			pluginId := strings.TrimSuffix(file.Name(), ".json")
			pluginSettingStore := setting.NewPluginSettingStore(tx, pluginId)

			pluginJsonPath := path.Join(pluginDir, file.Name())
			if _, err := os.Stat(pluginJsonPath); err != nil {
				continue
			}

			content, err := os.ReadFile(pluginJsonPath)
			if err != nil {
				continue
			}
			var setting struct {
				Name     string            `json:"Name"`
				Settings map[string]string `json:"Settings"`
			}
			if err := json.Unmarshal(content, &setting); err != nil {
				continue
			}
			util.GetLogger().Info(ctx, fmt.Sprintf("migrating plugin settings for %s (%s)", setting.Name, pluginId))

			counter := 0
			for key, value := range setting.Settings {
				if value == "" {
					continue
				}
				if err := pluginSettingStore.Set(key, value); err != nil {
					util.GetLogger().Warn(ctx, fmt.Sprintf("failed to migrate plugin setting %s for %s: %v", key, pluginId, err))
					continue
				}
				counter++
			}
			if err := os.Rename(pluginJsonPath, pluginJsonPath+".bak"); err != nil {
				util.GetLogger().Warn(ctx, fmt.Sprintf("failed to rename old plugin setting file to .bak for %s: %v", pluginId, err))
			}

			util.GetLogger().Info(ctx, fmt.Sprintf("migrated %d plugin settings for %s", counter, setting.Name))
		}
	}

	// Migrate query history
	if len(oldAppData.QueryHistories) > 0 {
		util.GetLogger().Info(ctx, fmt.Sprintf("migrating %d query histories", len(oldAppData.QueryHistories)))
		if err := woxSettingStore.Set("QueryHistories", oldAppData.QueryHistories); err != nil {
			util.GetLogger().Warn(ctx, fmt.Sprintf("failed to migrate query histories: %v", err))
		}
	}

	// Migrate favorite results
	if oldAppData.FavoriteResults != nil {
		util.GetLogger().Info(ctx, fmt.Sprintf("migrating %d favorite results", oldAppData.FavoriteResults.Len()))
		if err := woxSettingStore.Set("FavoriteResults", oldAppData.FavoriteResults); err != nil {
			util.GetLogger().Warn(ctx, fmt.Sprintf("failed to migrate favorite results: %v", err))
		}
	}

	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("failed to commit migration transaction: %w", err)
	}

	if _, err := os.Stat(oldSettingPath); err == nil {
		if err := os.Rename(oldSettingPath, oldSettingPath+".bak"); err != nil {
			util.GetLogger().Warn(ctx, fmt.Sprintf("Failed to rename old setting file to .bak: %v", err))
		}
	}
	if _, err := os.Stat(oldAppDataPath); err == nil {
		if err := os.Rename(oldAppDataPath, oldAppDataPath+".bak"); err != nil {
			util.GetLogger().Warn(ctx, fmt.Sprintf("Failed to rename old app data file to .bak: %v", err))
		}
	}

	// Migrate clipboard data
	if err := migrateClipboardData(ctx, tx); err != nil {
		util.GetLogger().Warn(ctx, fmt.Sprintf("failed to migrate clipboard data: %v", err))
	}

	util.GetLogger().Info(ctx, "Successfully migrated old configuration to the new database.")
	return nil
}

// Clipboard migration structures
type oldClipboardHistory struct {
	ID         string `json:"id"`
	Text       string `json:"text"`
	Type       string `json:"type"`
	Timestamp  int64  `json:"timestamp"`
	ImagePath  string `json:"imagePath,omitempty"`
	IsFavorite bool   `json:"isFavorite,omitempty"`
}

type favoriteClipboardItem struct {
	ID        string  `json:"id"`
	Type      string  `json:"type"`
	Content   string  `json:"content"`
	FilePath  string  `json:"filePath,omitempty"`
	IconData  *string `json:"iconData,omitempty"`
	Width     *int    `json:"width,omitempty"`
	Height    *int    `json:"height,omitempty"`
	FileSize  *int64  `json:"fileSize,omitempty"`
	Timestamp int64   `json:"timestamp"`
	CreatedAt int64   `json:"createdAt"`
}

type clipboardRecord struct {
	ID         string
	Type       string
	Content    string
	FilePath   string
	IconData   *string
	Width      *int
	Height     *int
	FileSize   *int64
	Timestamp  int64
	IsFavorite bool
	CreatedAt  time.Time
}

// migrateClipboardData migrates clipboard data from old JSON settings and database to new settings storage
func migrateClipboardData(ctx context.Context, tx *gorm.DB) error {
	clipboardPluginId := "5f815d98-27f5-488d-a756-c317ea39935b"
	pluginSettingStore := setting.NewPluginSettingStore(tx, clipboardPluginId)

	var allFavoritesToMigrate []favoriteClipboardItem

	// 1. Migrate from legacy JSON settings
	var historyJson string
	err := pluginSettingStore.Get("history", &historyJson)
	if err == nil && historyJson != "" {
		var history []oldClipboardHistory
		unmarshalErr := json.Unmarshal([]byte(historyJson), &history)
		if unmarshalErr != nil {
			// Log warning if logger is available
			if logger := util.GetLogger(); logger != nil {
				logger.Warn(ctx, fmt.Sprintf("failed to unmarshal legacy clipboard history: %v", unmarshalErr))
			}
		} else {
			// Migrate legacy data from JSON settings
			for _, item := range history {
				if item.IsFavorite {
					// Convert to new favorite format
					favoriteItem := favoriteClipboardItem{
						ID:        item.ID,
						Type:      item.Type,
						Content:   item.Text,
						FilePath:  item.ImagePath,
						Timestamp: item.Timestamp,
						CreatedAt: item.Timestamp / 1000, // Convert to seconds
					}
					allFavoritesToMigrate = append(allFavoritesToMigrate, favoriteItem)
				}
			}
			if logger := util.GetLogger(); logger != nil {
				logger.Info(ctx, fmt.Sprintf("found %d favorite items in legacy JSON settings", len(allFavoritesToMigrate)))
			}
		}
		// Clear the old history setting
		pluginSettingStore.Set("history", "")
	}

	// 2. Migrate existing favorite items from database
	dbFavorites, err := getFavoritesFromDatabase(ctx, clipboardPluginId)
	if err != nil {
		if logger := util.GetLogger(); logger != nil {
			logger.Warn(ctx, fmt.Sprintf("failed to get database favorites for migration: %v", err))
		}
	} else {
		for _, record := range dbFavorites {
			// Convert database record to favorite format
			favoriteItem := favoriteClipboardItem{
				ID:        record.ID,
				Type:      record.Type,
				Content:   record.Content,
				FilePath:  record.FilePath,
				IconData:  record.IconData,
				Width:     record.Width,
				Height:    record.Height,
				FileSize:  record.FileSize,
				Timestamp: record.Timestamp,
				CreatedAt: record.CreatedAt.Unix(),
			}
			allFavoritesToMigrate = append(allFavoritesToMigrate, favoriteItem)
		}

		if logger := util.GetLogger(); logger != nil {
			logger.Info(ctx, fmt.Sprintf("found %d favorite items in database", len(dbFavorites)))
		}

		// Remove favorite items from database after migration
		if len(dbFavorites) > 0 {
			deletedCount, deleteErr := deleteFavoritesFromDatabase(ctx, clipboardPluginId)
			if deleteErr != nil {
				if logger := util.GetLogger(); logger != nil {
					logger.Warn(ctx, fmt.Sprintf("failed to delete favorites from database: %v", deleteErr))
				}
			} else {
				if logger := util.GetLogger(); logger != nil {
					logger.Info(ctx, fmt.Sprintf("deleted %d favorite items from database after migration", deletedCount))
				}
			}
		}
	}

	// Save all favorites to new settings storage
	if len(allFavoritesToMigrate) > 0 {
		favoritesJson, err := json.Marshal(allFavoritesToMigrate)
		if err != nil {
			return fmt.Errorf("failed to marshal favorites: %w", err)
		}

		if err := pluginSettingStore.Set("favorites", string(favoritesJson)); err != nil {
			return fmt.Errorf("failed to save migrated favorites: %w", err)
		}

		if logger := util.GetLogger(); logger != nil {
			logger.Info(ctx, fmt.Sprintf("migrated %d favorite clipboard items to new storage", len(allFavoritesToMigrate)))
		}
	}

	return nil
}

// getFavoritesFromDatabase retrieves favorite clipboard items from the clipboard database
func getFavoritesFromDatabase(ctx context.Context, pluginId string) ([]clipboardRecord, error) {
	dbPath := path.Join(util.GetLocation().GetPluginSettingDirectory(), pluginId+"_clipboard.db")

	// Check if database file exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return []clipboardRecord{}, nil // No database file, no favorites to migrate
	}

	// Configure SQLite connection
	dsn := dbPath + "?" +
		"_journal_mode=WAL&" +
		"_synchronous=NORMAL&" +
		"_cache_size=1000&" +
		"_foreign_keys=true&" +
		"_busy_timeout=5000"

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open clipboard database: %w", err)
	}
	defer db.Close()

	querySQL := `
	SELECT id, type, content, file_path, icon_data, width, height, file_size, timestamp, is_favorite, created_at
	FROM clipboard_history
	WHERE is_favorite = TRUE
	ORDER BY timestamp DESC
	`

	rows, err := db.QueryContext(ctx, querySQL)
	if err != nil {
		return nil, fmt.Errorf("failed to query favorites: %w", err)
	}
	defer rows.Close()

	var records []clipboardRecord
	for rows.Next() {
		var record clipboardRecord
		err := rows.Scan(&record.ID, &record.Type, &record.Content,
			&record.FilePath, &record.IconData, &record.Width, &record.Height, &record.FileSize,
			&record.Timestamp, &record.IsFavorite, &record.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan record: %w", err)
		}
		records = append(records, record)
	}

	return records, rows.Err()
}

// deleteFavoritesFromDatabase removes all favorite items from the clipboard database
func deleteFavoritesFromDatabase(ctx context.Context, pluginId string) (int64, error) {
	dbPath := path.Join(util.GetLocation().GetPluginSettingDirectory(), pluginId+"_clipboard.db")

	// Check if database file exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return 0, nil // No database file, nothing to delete
	}

	// Configure SQLite connection
	dsn := dbPath + "?" +
		"_journal_mode=WAL&" +
		"_synchronous=NORMAL&" +
		"_cache_size=1000&" +
		"_foreign_keys=true&" +
		"_busy_timeout=5000"

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return 0, fmt.Errorf("failed to open clipboard database: %w", err)
	}
	defer db.Close()

	deleteSQL := `DELETE FROM clipboard_history WHERE is_favorite = TRUE`
	result, err := db.ExecContext(ctx, deleteSQL)
	if err != nil {
		return 0, fmt.Errorf("failed to delete favorites: %w", err)
	}

	return result.RowsAffected()
}
