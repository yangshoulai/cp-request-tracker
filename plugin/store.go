package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	_ "modernc.org/sqlite"
)

type callRecord struct {
	ID              int64     `json:"id"`
	RequestID       string    `json:"request_id,omitempty"`
	TraceID         string    `json:"trace_id,omitempty"`
	AccountType     string    `json:"account_type"`
	AccountEmail    string    `json:"account_email,omitempty"`
	RequestedAt     time.Time `json:"requested_at"`
	LatencyMS       int64     `json:"latency_ms"`
	Model           string    `json:"model"`
	ReasoningEffort string    `json:"reasoning_effort,omitempty"`
	Failed          bool      `json:"failed"`
	StatusCode      int       `json:"status_code,omitempty"`
}

type callFilter struct {
	Page            int
	PageSize        int
	AccountType     string
	Email           string
	Model           string
	ReasoningEffort string
	Keyword         string
}

type callPage struct {
	Items         []callRecord `json:"items"`
	Total         int64        `json:"total"`
	Page          int          `json:"page"`
	PageSize      int          `json:"page_size"`
	RetentionDays int          `json:"retention_days"`
	Emails        []string     `json:"emails"`
}

type authProviderMaps struct {
	byKey          map[string]string
	byID           map[string]string
	keysByProvider map[string]map[string]struct{}
	idsByProvider  map[string]map[string]struct{}
}

func normalizeProviderName(value string) string {
	value = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(value, "_", "-")))
	switch value {
	case "x-ai", "grok":
		return "xai"
	case "muse":
		return "meta"
	default:
		return value
	}
}

func authProviderKey(authID, authIndex string) string {
	return strings.TrimSpace(authID) + "\x00" + strings.TrimSpace(authIndex)
}

func buildAuthProviderMaps(entries []pluginapi.HostAuthFileEntry) authProviderMaps {
	result := authProviderMaps{
		byKey:          make(map[string]string),
		byID:           make(map[string]string),
		keysByProvider: make(map[string]map[string]struct{}),
		idsByProvider:  make(map[string]map[string]struct{}),
	}
	for _, entry := range entries {
		authID := strings.TrimSpace(entry.ID)
		if authID == "" {
			continue
		}
		provider := normalizeProviderName(entry.Provider)
		if provider == "" {
			provider = normalizeProviderName(entry.Type)
		}
		if provider == "" {
			continue
		}
		result.byID[authID] = provider
		key := authProviderKey(authID, entry.AuthIndex)
		result.byKey[key] = provider
		if result.keysByProvider[provider] == nil {
			result.keysByProvider[provider] = make(map[string]struct{})
		}
		result.keysByProvider[provider][key] = struct{}{}
		if result.idsByProvider[provider] == nil {
			result.idsByProvider[provider] = make(map[string]struct{})
		}
		result.idsByProvider[provider][authID] = struct{}{}
	}
	return result
}

var (
	storeMu       sync.Mutex
	storeDB       *sql.DB
	storeOpenErr  error
	retentionDays atomic.Int64
)

func setRetentionDays(days int) {
	retentionDays.Store(int64(days))
}

func currentRetentionDays() int {
	days := int(retentionDays.Load())
	if days < 1 {
		return 7
	}
	return days
}

func openStore() (*sql.DB, error) {
	storeMu.Lock()
	defer storeMu.Unlock()
	if storeDB != nil {
		return storeDB, nil
	}
	if storeOpenErr != nil {
		return nil, storeOpenErr
	}
	base, errConfigDir := os.UserConfigDir()
	if errConfigDir != nil || strings.TrimSpace(base) == "" {
		storeOpenErr = fmt.Errorf("resolve user configuration directory: %w", errConfigDir)
		return nil, storeOpenErr
	}
	dir := filepath.Join(base, "CLIProxyAPI", "plugins", pluginID)
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		storeOpenErr = fmt.Errorf("create plugin data directory: %w", errMkdir)
		return nil, storeOpenErr
	}
	if errChmod := os.Chmod(dir, 0o700); errChmod != nil {
		storeOpenErr = fmt.Errorf("secure plugin data directory: %w", errChmod)
		return nil, storeOpenErr
	}
	db, errOpen := sql.Open("sqlite", filepath.Join(dir, "calls.sqlite"))
	if errOpen != nil {
		storeOpenErr = fmt.Errorf("open SQLite database: %w", errOpen)
		return nil, storeOpenErr
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, statement := range []string{
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		`CREATE TABLE IF NOT EXISTS calls (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			request_id TEXT NOT NULL DEFAULT '',
			trace_id TEXT NOT NULL DEFAULT '',
			auth_id TEXT NOT NULL DEFAULT '',
			auth_index TEXT NOT NULL DEFAULT '',
			account_type TEXT NOT NULL DEFAULT '',
			requested_at_ms INTEGER NOT NULL,
			latency_ms INTEGER NOT NULL DEFAULT 0,
			model TEXT NOT NULL DEFAULT '',
			reasoning_effort TEXT NOT NULL DEFAULT '',
			failed INTEGER NOT NULL DEFAULT 0,
			status_code INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS accounts (
			auth_id TEXT NOT NULL DEFAULT '',
			auth_index TEXT NOT NULL DEFAULT '',
			email TEXT NOT NULL DEFAULT '',
			updated_at_ms INTEGER NOT NULL,
			PRIMARY KEY (auth_id, auth_index)
		)`,
		"CREATE INDEX IF NOT EXISTS idx_calls_requested_at ON calls(requested_at_ms DESC, id DESC)",
		"CREATE INDEX IF NOT EXISTS idx_calls_account_type ON calls(account_type)",
		"CREATE INDEX IF NOT EXISTS idx_calls_model ON calls(model)",
	} {
		if _, errExec := db.Exec(statement); errExec != nil {
			_ = db.Close()
			storeOpenErr = fmt.Errorf("initialize SQLite database: %w", errExec)
			return nil, storeOpenErr
		}
	}
	storeDB = db
	return storeDB, nil
}

func closeStore() {
	storeMu.Lock()
	defer storeMu.Unlock()
	if storeDB != nil {
		_ = storeDB.Close()
	}
	storeDB = nil
	storeOpenErr = nil
}

func saveCall(record pluginapi.UsageRecord) error {
	db, errOpen := openStore()
	if errOpen != nil {
		return errOpen
	}
	requestedAt := record.RequestedAt
	if requestedAt.IsZero() {
		requestedAt = time.Now()
	}
	// 页面中的“账号类型”对应 CPA 的提供方（codex、xai 等），而不是
	// AuthType 的认证方式（oauth、apikey 等）。
	accountType := normalizeProviderName(record.Provider)
	if accountType == "" {
		accountType = normalizeProviderName(record.AuthType)
	}
	_, errInsert := db.Exec(`INSERT INTO calls (
		request_id, trace_id, auth_id, auth_index, account_type, requested_at_ms,
		latency_ms, model, reasoning_effort, failed, status_code
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(record.RequestID), strings.TrimSpace(record.TraceID),
		strings.TrimSpace(record.AuthID), strings.TrimSpace(record.AuthIndex), accountType,
		requestedAt.UnixMilli(), max(record.Latency.Milliseconds(), 0), strings.TrimSpace(record.Model),
		strings.TrimSpace(record.ReasoningEffort), record.Failed, record.Failure.StatusCode,
	)
	if errInsert != nil {
		return fmt.Errorf("save request metadata: %w", errInsert)
	}
	return pruneCalls(db, time.Now())
}

func pruneCalls(db *sql.DB, now time.Time) error {
	cutoff := now.Add(-time.Duration(currentRetentionDays()) * 24 * time.Hour).UnixMilli()
	if _, errDelete := db.Exec("DELETE FROM calls WHERE requested_at_ms < ?", cutoff); errDelete != nil {
		return fmt.Errorf("remove expired request metadata: %w", errDelete)
	}
	if _, errDelete := db.Exec("DELETE FROM accounts WHERE updated_at_ms < ? AND NOT EXISTS (SELECT 1 FROM calls WHERE calls.auth_id = accounts.auth_id AND calls.auth_index = accounts.auth_index)", cutoff); errDelete != nil {
		return fmt.Errorf("remove expired account metadata: %w", errDelete)
	}
	return nil
}

func listCalls(ctx context.Context, filter callFilter, authFiles []pluginapi.HostAuthFileEntry) (callPage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	db, errOpen := openStore()
	if errOpen != nil {
		return callPage{}, errOpen
	}
	if errPrune := pruneCalls(db, time.Now()); errPrune != nil {
		return callPage{}, errPrune
	}
	if errSync := syncAccountEmails(ctx, db, authFiles); errSync != nil {
		return callPage{}, errSync
	}
	emails, errEmails := listEmailOptions(ctx, db, authFiles)
	if errEmails != nil {
		return callPage{}, errEmails
	}
	providers := buildAuthProviderMaps(authFiles)
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PageSize < 1 {
		filter.PageSize = 25
	}
	if filter.PageSize > 100 {
		filter.PageSize = 100
	}
	where := []string{"1 = 1"}
	args := make([]any, 0, 10)
	addLike := func(expression, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		where = append(where, expression+" LIKE ?")
		args = append(args, "%"+value+"%")
	}
	if accountType := strings.TrimSpace(filter.AccountType); accountType != "" {
		provider := normalizeProviderName(accountType)
		clauses := []string{"LOWER(c.account_type) LIKE LOWER(?)"}
		args = append(args, "%"+accountType+"%")
		for key := range providers.keysByProvider[provider] {
			parts := strings.SplitN(key, "\x00", 2)
			if len(parts) != 2 {
				continue
			}
			if parts[1] == "" {
				clauses = append(clauses, "c.auth_id = ?")
				args = append(args, parts[0])
			} else {
				clauses = append(clauses, "(c.auth_id = ? AND c.auth_index = ?)")
				args = append(args, parts[0], parts[1])
			}
		}
		for authID := range providers.idsByProvider[provider] {
			clauses = append(clauses, "c.auth_id = ?")
			args = append(args, authID)
		}
		where = append(where, "("+strings.Join(clauses, " OR ")+")")
	}
	addLike("coalesce(a.email, '')", filter.Email)
	addLike("c.model", filter.Model)
	addLike("c.reasoning_effort", filter.ReasoningEffort)
	if strings.TrimSpace(filter.Keyword) != "" {
		where = append(where, "(c.request_id LIKE ? OR c.trace_id LIKE ? OR c.auth_id LIKE ? OR c.auth_index LIKE ? OR c.model LIKE ? OR coalesce(a.email, '') LIKE ?)")
		pattern := "%" + strings.TrimSpace(filter.Keyword) + "%"
		for range 6 {
			args = append(args, pattern)
		}
	}
	clause := strings.Join(where, " AND ")
	var total int64
	countQuery := `SELECT count(*) FROM calls c LEFT JOIN accounts a ON a.auth_id = c.auth_id AND a.auth_index = c.auth_index WHERE ` + clause
	if errCount := db.QueryRowContext(ctx, countQuery, args...).Scan(&total); errCount != nil {
		return callPage{}, fmt.Errorf("count request metadata: %w", errCount)
	}
	queryArgs := append([]any(nil), args...)
	queryArgs = append(queryArgs, filter.PageSize, (filter.Page-1)*filter.PageSize)
	query := `SELECT c.id, c.request_id, c.trace_id, c.auth_id, c.auth_index, c.account_type, coalesce(a.email, ''),
		c.requested_at_ms, c.latency_ms, c.model, c.reasoning_effort, c.failed, c.status_code
		FROM calls c LEFT JOIN accounts a ON a.auth_id = c.auth_id AND a.auth_index = c.auth_index
		WHERE ` + clause + " ORDER BY c.requested_at_ms DESC, c.id DESC LIMIT ? OFFSET ?"
	rows, errQuery := db.QueryContext(ctx, query, queryArgs...)
	if errQuery != nil {
		return callPage{}, fmt.Errorf("query request metadata: %w", errQuery)
	}
	defer rows.Close()
	items := make([]callRecord, 0, filter.PageSize)
	for rows.Next() {
		var item callRecord
		var authID, authIndex string
		var requestedAtMS int64
		if errScan := rows.Scan(&item.ID, &item.RequestID, &item.TraceID, &authID, &authIndex, &item.AccountType, &item.AccountEmail, &requestedAtMS, &item.LatencyMS, &item.Model, &item.ReasoningEffort, &item.Failed, &item.StatusCode); errScan != nil {
			return callPage{}, fmt.Errorf("read request metadata: %w", errScan)
		}
		if provider := providers.byKey[authProviderKey(authID, authIndex)]; provider != "" {
			item.AccountType = provider
		} else if provider := providers.byKey[authProviderKey(authID, "")]; provider != "" {
			item.AccountType = provider
		} else if provider := providers.byID[authID]; provider != "" {
			item.AccountType = provider
		}
		item.RequestedAt = time.UnixMilli(requestedAtMS).UTC()
		items = append(items, item)
	}
	if errRows := rows.Err(); errRows != nil {
		return callPage{}, fmt.Errorf("iterate request metadata: %w", errRows)
	}
	return callPage{Items: items, Total: total, Page: filter.Page, PageSize: filter.PageSize, RetentionDays: currentRetentionDays(), Emails: emails}, nil
}

func listEmailOptions(ctx context.Context, db *sql.DB, entries []pluginapi.HostAuthFileEntry) ([]string, error) {
	emails := make(map[string]string)
	for _, entry := range entries {
		email := strings.TrimSpace(entry.Email)
		if email != "" {
			emails[strings.ToLower(email)] = email
		}
	}
	rows, errQuery := db.QueryContext(ctx, `SELECT DISTINCT trim(email) FROM accounts WHERE trim(email) <> ''`)
	if errQuery != nil {
		return nil, fmt.Errorf("list account email options: %w", errQuery)
	}
	defer rows.Close()
	for rows.Next() {
		var email string
		if errScan := rows.Scan(&email); errScan != nil {
			return nil, fmt.Errorf("read account email option: %w", errScan)
		}
		email = strings.TrimSpace(email)
		if email != "" {
			emails[strings.ToLower(email)] = email
		}
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, fmt.Errorf("iterate account email options: %w", errRows)
	}
	options := make([]string, 0, len(emails))
	for _, email := range emails {
		options = append(options, email)
	}
	sort.Slice(options, func(i, j int) bool {
		return strings.ToLower(options[i]) < strings.ToLower(options[j])
	})
	return options, nil
}

func syncAccountEmails(ctx context.Context, db *sql.DB, entries []pluginapi.HostAuthFileEntry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, errBegin := db.BeginTx(ctx, nil)
	if errBegin != nil {
		return fmt.Errorf("begin account metadata sync: %w", errBegin)
	}
	defer func() { _ = tx.Rollback() }()
	updatedAt := time.Now().UnixMilli()
	for _, entry := range entries {
		authID, authIndex := strings.TrimSpace(entry.ID), strings.TrimSpace(entry.AuthIndex)
		if authID == "" && authIndex == "" {
			continue
		}
		if _, errExec := tx.ExecContext(ctx, `INSERT INTO accounts (auth_id, auth_index, email, updated_at_ms)
			VALUES (?, ?, ?, ?) ON CONFLICT(auth_id, auth_index) DO UPDATE SET
			email = excluded.email, updated_at_ms = excluded.updated_at_ms`, authID, authIndex, strings.TrimSpace(entry.Email), updatedAt); errExec != nil {
			return fmt.Errorf("sync account email: %w", errExec)
		}
	}
	if errCommit := tx.Commit(); errCommit != nil && !errors.Is(errCommit, sql.ErrTxDone) {
		return fmt.Errorf("commit account metadata sync: %w", errCommit)
	}
	return nil
}
