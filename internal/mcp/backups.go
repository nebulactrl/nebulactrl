package mcp

import (
	"context"
	"encoding/json"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/backups"
	"github.com/nebulactrl/nebulactrl/internal/mcp/mcpkit"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type listBackupsInput struct {
	ServiceID string `json:"serviceId" jsonschema:"the managed database: uuid or project/env/service"`
	Limit     int    `json:"limit,omitempty" jsonschema:"maximum number of backups (default 50, at most 200)"`
}

type createBackupInput struct {
	ServiceID     string `json:"serviceId" jsonschema:"the managed database: uuid or project/env/service"`
	DestinationID string `json:"destinationId" jsonschema:"id (uuid) of a backup destination; list them with list_backup_destinations"`
}

type restoreBackupInput struct {
	BackupID        string `json:"backupId" jsonschema:"id (uuid) of a succeeded backup from list_backups"`
	ConfirmBackupID string `json:"confirmBackupId" jsonschema:"must equal backupId; confirm with the user before restoring"`
	Name            string `json:"name,omitempty" jsonschema:"name of the new database; default <database>-restored-<time>"`
}

// backupTools are the database backup tools. Restores only ever go into a NEW
// sibling database (the original is never overwritten through these tools;
// restoreBackup in_place stays a REST/UI action reachable through
// call_operation behind the production approval).
func (h *Handler) backupTools() []mcpkit.Tool {
	d := h.deps
	if d.Backups == nil {
		return nil
	}
	read := mcpkit.Hints{ReadOnly: true}
	return []mcpkit.Tool{
		{
			Name: "list_backup_destinations", Toolset: mcpkit.ToolsetData, Hints: read,
			Description: "List the backup destinations (S3-compatible buckets and server directories). Credentials are never returned.",
			Scopes:      []string{policy.ScopeProjectsRead},
			Input:       struct{}{},
			Call: func(ctx context.Context, _ *auth.Principal, _ json.RawMessage) (any, error) {
				items, err := d.Backups.ListDestinations(ctx)
				if err != nil {
					return nil, err
				}
				return fitList(newList(items, "")), nil
			},
		},
		{
			Name: "list_backups", Toolset: mcpkit.ToolsetData, Hints: read,
			Description: "List a managed database's backups, newest first, with status (queued, running, succeeded, failed, skipped, expired), size and destination.",
			Scopes:      []string{policy.ScopeProjectsRead},
			Input:       listBackupsInput{},
			Call: func(ctx context.Context, _ *auth.Principal, raw json.RawMessage) (any, error) {
				in, err := mcpkit.Decode[listBackupsInput](raw)
				if err != nil {
					return nil, err
				}
				id, err := h.resolveService(ctx, in.ServiceID)
				if err != nil {
					return nil, err
				}
				items, err := d.Backups.ListBackups(ctx, id, in.Limit)
				if err != nil {
					return nil, err
				}
				return fitList(newList(items, "")), nil
			},
		},
		{
			Name: "create_backup", Toolset: mcpkit.ToolsetData, Hints: mcpkit.Hints{OpenWorld: true},
			Description: "Start a backup of a managed database to a destination. Returns the queued backup; poll list_backups for the outcome.",
			Scopes:      []string{policy.ScopeDeploymentsWrite},
			Input:       createBackupInput{},
			Call: func(ctx context.Context, _ *auth.Principal, raw json.RawMessage) (any, error) {
				in, err := mcpkit.Decode[createBackupInput](raw)
				if err != nil {
					return nil, err
				}
				sid, err := h.resolveService(ctx, in.ServiceID)
				if err != nil {
					return nil, err
				}
				did, err := parseID("destinationId", in.DestinationID)
				if err != nil {
					return nil, err
				}
				return d.Backups.CreateBackup(ctx, sid, did)
			},
		},
		{
			Name: "restore_backup", Toolset: mcpkit.ToolsetData, Hints: mcpkit.Hints{OpenWorld: true},
			Description: "Restore a backup into a NEW sibling database (same engine and version, fresh credentials). The original database is not touched. Requires confirmBackupId equal to backupId, which you must confirm with the user first.",
			Scopes:      []string{policy.ScopeDeploymentsWrite, policy.ScopeProjectsWrite},
			Input:       restoreBackupInput{},
			Call: func(ctx context.Context, _ *auth.Principal, raw json.RawMessage) (any, error) {
				in, err := mcpkit.Decode[restoreBackupInput](raw)
				if err != nil {
					return nil, err
				}
				bid, err := parseID("backupId", in.BackupID)
				if err != nil {
					return nil, err
				}
				confirm, err := parseID("confirmBackupId", in.ConfirmBackupID)
				if err != nil {
					return nil, err
				}
				if confirm != bid {
					return nil, toolErr(CodeInvalidArgument, "confirmBackupId must equal backupId")
				}
				return d.Backups.Restore(ctx, bid, backups.RestoreInput{Mode: backups.ModeNew, Name: in.Name})
			},
		},
	}
}
