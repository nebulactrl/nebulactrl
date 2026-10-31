package teams

import (
	"context"
	"net/http"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type listTeamsOutput struct {
	Body struct {
		Items []Team `json:"items"`
	}
}

type teamOutput struct{ Body Team }

type teamIDInput struct {
	ID uuid.UUID `path:"teamId"`
}

type createTeamInput struct {
	Body struct {
		Name string `json:"name" minLength:"1" maxLength:"80"`
	}
}

type renameTeamInput struct {
	ID   uuid.UUID `path:"teamId"`
	Body struct {
		Name string `json:"name" minLength:"1" maxLength:"80"`
	}
}

type deleteTeamInput struct {
	ID             uuid.UUID `path:"teamId"`
	Confirm        string    `query:"confirm" doc:"The team's slug"`
	KeepDNSRecords bool      `query:"keepDnsRecords" doc:"Leave the DNS records NebulaCtrl created in Cloudflare instead of deleting them"`
}

type deletionPlanOutput struct{ Body TeamDeletionPlan }

type listMembersOutput struct {
	Body struct {
		Items []TeamMember `json:"items"`
	}
}

type memberPathInput struct {
	ID     uuid.UUID `path:"teamId"`
	UserID uuid.UUID `path:"userId"`
}

type updateMemberInput struct {
	ID     uuid.UUID `path:"teamId"`
	UserID uuid.UUID `path:"userId"`
	Body   struct {
		Role        *string      `json:"role,omitempty" enum:"owner,admin,developer,viewer"`
		ProjectIDs  *[]uuid.UUID `json:"projectIds,omitempty" doc:"Restrict the member to these projects (developer and viewer only); an empty list sees no project"`
		AllProjects *bool        `json:"allProjects,omitempty" doc:"true lifts the project restriction"`
	}
}

type memberOutput struct{ Body TeamMember }

type passwordResetLinkOutput struct{ Body PasswordResetLink }

type createInviteInput struct {
	ID   uuid.UUID `path:"teamId"`
	Body struct {
		Email         string      `json:"email,omitempty" maxLength:"254" doc:"Mail the invite to this address (single use). Omit for a link invite."`
		Role          string      `json:"role" enum:"owner,admin,developer,viewer"`
		ProjectIDs    []uuid.UUID `json:"projectIds,omitempty" doc:"Restrict the new member to these projects (developer and viewer only)"`
		MaxUses       int         `json:"maxUses,omitempty" minimum:"0" maximum:"1000" doc:"Link invites: how many people may use it (default 1)"`
		ExpiresInDays int         `json:"expiresInDays,omitempty" minimum:"0" maximum:"90" doc:"Default 7"`
	}
}

type createInviteOutput struct{ Body CreatedInvite }

type listInvitesOutput struct {
	Body struct {
		Items []TeamInvite `json:"items"`
	}
}

type invitePathInput struct {
	ID       uuid.UUID `path:"teamId"`
	InviteID uuid.UUID `path:"inviteId"`
}

type inviteTokenInput struct {
	Token string `path:"token" maxLength:"128"`
}

type previewOutput struct{ Body InvitePreview }

type acceptInviteInput struct {
	Token string `path:"token" maxLength:"128"`
	Body  *struct {
		Email    string `json:"email,omitempty" maxLength:"254" doc:"Sign-up only"`
		Name     string `json:"name,omitempty" maxLength:"100" doc:"Sign-up only"`
		Password string `json:"password,omitempty" maxLength:"256" doc:"Sign-up only: creates the account"`
	} `required:"false" doc:"Omit when signed in"`
}

type acceptOutput struct{ Body AcceptedInvite }

type instanceOutput struct{ Body InstanceSettings }

type updateInstanceInput struct {
	Body struct {
		AllowTeamCreation      *bool   `json:"allowTeamCreation,omitempty"`
		SMTPHost               *string `json:"smtpHost,omitempty" maxLength:"253"`
		SMTPPort               *int    `json:"smtpPort,omitempty" minimum:"1" maximum:"65535"`
		SMTPTLS                *string `json:"smtpTls,omitempty" enum:"implicit,starttls,none"`
		SMTPUsername           *string `json:"smtpUsername,omitempty" maxLength:"254"`
		SMTPPassword           *string `json:"smtpPassword,omitempty" maxLength:"1024" doc:"Write-only. Empty clears it."`
		SMTPFrom               *string `json:"smtpFrom,omitempty" maxLength:"254"`
		SMTPInsecureSkipVerify *bool   `json:"smtpInsecureSkipVerify,omitempty"`
	}
}

// Register attaches the team, member, invite and instance endpoints. sessions
// stores the current team on switch and signs invitees in after sign-up.
func Register(h huma.API, svc *Service, sessions *scs.SessionManager) {
	previews := api.NewIPLimiter(60, time.Minute)
	accepts := api.NewIPLimiter(10, time.Minute)
	stdErrs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listTeams", Method: http.MethodGet, Path: "/teams", Tags: []string{"teams"},
		Summary: "List the caller's teams", Description: "Every team the signed-in user belongs to, with their role in each. `current` marks the team the request acts in.",
		Errors: []int{http.StatusUnauthorized},
	}), func(ctx context.Context, _ *struct{}) (*listTeamsOutput, error) {
		items, err := svc.List(ctx)
		if err != nil {
			return nil, err
		}
		out := &listTeamsOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "createTeam", Method: http.MethodPost, Path: "/teams", Tags: []string{"teams"},
		Summary: "Create a team", Description: "The caller becomes its owner. 403 `team_creation_disabled` when the instance admin turned team creation off.",
		DefaultStatus: http.StatusCreated,
		Errors:        []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity},
	}), policy.ScopeAdmin), func(ctx context.Context, in *createTeamInput) (*teamOutput, error) {
		t, err := svc.Create(ctx, in.Body.Name)
		if err != nil {
			return nil, err
		}
		return &teamOutput{Body: t}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getTeam", Method: http.MethodGet, Path: "/teams/{teamId}", Tags: []string{"teams"},
		Summary: "Get a team", Errors: stdErrs,
	}), func(ctx context.Context, in *teamIDInput) (*teamOutput, error) {
		t, err := svc.Get(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &teamOutput{Body: t}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateTeam", Method: http.MethodPatch, Path: "/teams/{teamId}", Tags: []string{"teams"},
		Summary: "Rename a team", Errors: append(stdErrs, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *renameTeamInput) (*teamOutput, error) {
		t, err := svc.Rename(ctx, in.ID, in.Body.Name)
		if err != nil {
			return nil, err
		}
		return &teamOutput{Body: t}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteTeam", Method: http.MethodDelete, Path: "/teams/{teamId}", Tags: []string{"teams"},
		Summary: "Delete a team with everything in it",
		Description: "Owners only, signed in as a person. Requires `confirm=<slug>`. Starts a background job and answers 202 with the team: its `deletion` says " +
			"where the job is. It deletes every project with its services, volumes and domains, removes the platform services, then every server " +
			"(the agent of an online server empties its host and removes itself; an offline server is removed without touching its host and " +
			"listed in `deletion.forgottenServers`), the status pages, the Cloudflare connection (the DNS records NebulaCtrl created are deleted " +
			"unless `keepDnsRecords=true`), the tailnet integration with the devices and keys NebulaCtrl created, and finally the team. Backups " +
			"already written to backup destinations stay there. If a step fails the team stays, `deletion.state` is `failed` with the reason, and " +
			"deleting again retries. 409 `team_hosts_control_plane` when the team owns the server NebulaCtrl runs on, 409 `deletion_in_progress`. " +
			"Denied to MCP agents.",
		DefaultStatus: http.StatusAccepted,
		Errors:        append(stdErrs, http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *deleteTeamInput) (*teamOutput, error) {
		t, err := svc.Delete(ctx, in.ID, DeleteInput{Confirm: in.Confirm, KeepDNSRecords: in.KeepDNSRecords})
		if err != nil {
			return nil, err
		}
		return &teamOutput{Body: t}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getTeamDeletionPlan", Method: http.MethodGet, Path: "/teams/{teamId}/deletion-plan", Tags: []string{"teams"},
		Summary: "What deleting a team removes",
		Description: "Owners only. Counts of the team's projects, services, databases, volumes, servers (online and offline), domains, status pages, " +
			"DNS records NebulaCtrl manages and tailnet devices NebulaCtrl created. Nothing is deleted.",
		Errors: stdErrs,
	}, policy.ScopeAdmin), func(ctx context.Context, in *teamIDInput) (*deletionPlanOutput, error) {
		p, err := svc.DeletionPlan(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &deletionPlanOutput{Body: p}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "switchTeam", Method: http.MethodPost, Path: "/teams/{teamId}/switch", Tags: []string{"teams"},
		Summary:     "Make a team the session's current team",
		Description: "Re-checks membership (404 for non-members) and stores the team in the session. Browser sessions only: an API token belongs to one team.",
		Errors:      stdErrs,
	}), func(ctx context.Context, in *teamIDInput) (*teamOutput, error) {
		t, err := svc.Switch(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		api.SetSessionTeam(ctx, sessions, t.ID)
		return &teamOutput{Body: t}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listTeamMembers", Method: http.MethodGet, Path: "/teams/{teamId}/members", Tags: []string{"teams"},
		Summary: "List a team's members", Errors: stdErrs,
	}), func(ctx context.Context, in *teamIDInput) (*listMembersOutput, error) {
		items, err := svc.Members(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		out := &listMembersOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateTeamMember", Method: http.MethodPatch, Path: "/teams/{teamId}/members/{userId}", Tags: []string{"teams"},
		Summary:     "Change a member's role or project restriction",
		Description: "Owners and admins; only owners manage owners. 409 `last_owner` when the change would leave the team without an owner.",
		Errors:      append(stdErrs, http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *updateMemberInput) (*memberOutput, error) {
		patch := MemberPatch{Role: in.Body.Role}
		switch {
		case in.Body.AllProjects != nil && *in.Body.AllProjects:
			patch.SetProjects = true
		case in.Body.ProjectIDs != nil:
			patch.SetProjects, patch.ProjectIDs = true, append([]uuid.UUID{}, *in.Body.ProjectIDs...)
		}
		m, err := svc.UpdateMember(ctx, in.ID, in.UserID, patch)
		if err != nil {
			return nil, err
		}
		return &memberOutput{Body: m}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "removeTeamMember", Method: http.MethodDelete, Path: "/teams/{teamId}/members/{userId}", Tags: []string{"teams"},
		Summary:       "Remove a member (or leave the team)",
		Description:   "Owners and admins remove members; any member may remove themselves. 409 `last_owner` for the last owner.",
		DefaultStatus: http.StatusNoContent,
		Errors:        append(stdErrs, http.StatusConflict),
	}, policy.ScopeAdmin), func(ctx context.Context, in *memberPathInput) (*struct{}, error) {
		return nil, svc.RemoveMember(ctx, in.ID, in.UserID)
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "createMemberPasswordResetLink", Method: http.MethodPost, Path: "/teams/{teamId}/members/{userId}/password-reset", Tags: []string{"teams"},
		Summary: "Generate a password reset link for a member",
		Description: "Owners and admins (admins cannot reset an owner); signed-in users only. The link works once for 30 minutes, needs no SMTP setup " +
			"and is shown once. Using it ends every session of the member; their API tokens keep working unless they choose to revoke them. " +
			"409 `no_password` for members who only sign in through an identity provider. A member who belongs to another team can be reset only by the instance admin " +
			"(409 `member_of_other_teams`); the instance admin's own password is reset on the server (403 `instance_admin_account`).",
		DefaultStatus: http.StatusCreated,
		Errors:        append(stdErrs, http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *memberPathInput) (*passwordResetLinkOutput, error) {
		l, err := svc.CreatePasswordResetLink(ctx, in.ID, in.UserID)
		if err != nil {
			return nil, err
		}
		return &passwordResetLinkOutput{Body: l}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "createTeamInvite", Method: http.MethodPost, Path: "/teams/{teamId}/invites", Tags: []string{"teams"},
		Summary: "Invite someone to a team",
		Description: "With `email` the link is mailed (team email channel, else the instance SMTP relay) and works once for that address; " +
			"without it a link invite is created with an expiry and a use limit. `delivery` tells whether the email went out; the response always carries the link.",
		DefaultStatus: http.StatusCreated,
		Errors:        append(stdErrs, http.StatusConflict, http.StatusUnprocessableEntity),
	}), policy.ScopeAdmin), func(ctx context.Context, in *createInviteInput) (*createInviteOutput, error) {
		c, err := svc.CreateInvite(ctx, in.ID, InviteInput{
			Email: in.Body.Email, Role: in.Body.Role, ProjectIDs: in.Body.ProjectIDs, MaxUses: in.Body.MaxUses,
			TTL: time.Duration(in.Body.ExpiresInDays) * 24 * time.Hour,
		})
		if err != nil {
			return nil, err
		}
		return &createInviteOutput{Body: c}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listTeamInvites", Method: http.MethodGet, Path: "/teams/{teamId}/invites", Tags: []string{"teams"},
		Summary: "List a team's pending invites", Errors: stdErrs,
	}, policy.ScopeAdmin), func(ctx context.Context, in *teamIDInput) (*listInvitesOutput, error) {
		items, err := svc.Invites(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		out := &listInvitesOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteTeamInvite", Method: http.MethodDelete, Path: "/teams/{teamId}/invites/{inviteId}", Tags: []string{"teams"},
		Summary: "Revoke an invite", DefaultStatus: http.StatusNoContent, Errors: stdErrs,
	}, policy.ScopeAdmin), func(ctx context.Context, in *invitePathInput) (*struct{}, error) {
		return nil, svc.RevokeInvite(ctx, in.ID, in.InviteID)
	})

	huma.Register(h, api.Public(huma.Operation{
		OperationID: "previewInvite", Method: http.MethodGet, Path: "/invites/{token}", Tags: []string{"teams"},
		Summary:     "Preview an invite (public)",
		Description: "What the invite link offers: team, role, expiry. 404 `invite_invalid`, 410 `invite_expired` or `invite_used` otherwise. Rate limited per IP.",
		Errors:      []int{http.StatusNotFound, http.StatusGone, http.StatusTooManyRequests},
	}), func(ctx context.Context, in *inviteTokenInput) (*previewOutput, error) {
		if !previews.Allow(api.ClientIP(ctx)) {
			return nil, apierr.TooManyRequests("too many attempts; try again in a minute")
		}
		p, err := svc.PreviewInvite(ctx, in.Token)
		if err != nil {
			return nil, err
		}
		return &previewOutput{Body: p}, nil
	})

	huma.Register(h, api.Public(huma.Operation{
		OperationID: "acceptInvite", Method: http.MethodPost, Path: "/invites/{token}/accept", Tags: []string{"teams"},
		Summary: "Accept an invite",
		Description: "Signed in: joins the team and makes it the session's current team. Not signed in: send `name` and `password` (and `email` for link invites) " +
			"to create the account and join in one step; the session is started. 409 `account_exists` when the email already has an account (sign in instead), " +
			"403 `invite_email_mismatch` for an invite sent to another address.",
		Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusGone, http.StatusUnprocessableEntity, http.StatusTooManyRequests},
	}), func(ctx context.Context, in *acceptInviteInput) (*acceptOutput, error) {
		if !accepts.Allow(api.ClientIP(ctx)) {
			return nil, apierr.TooManyRequests("too many attempts; try again in a minute")
		}
		if p, ok := auth.PrincipalFrom(ctx); ok && p.Type == auth.TypeUser {
			res, err := svc.Accept(ctx, in.Token)
			if err != nil {
				return nil, err
			}
			api.SetSessionTeam(ctx, sessions, res.Team.ID)
			return &acceptOutput{Body: res}, nil
		}
		if in.Body == nil || in.Body.Password == "" {
			return nil, apierr.Unauthorized()
		}
		res, err := svc.AcceptWithSignup(ctx, in.Token, SignupInput{Email: in.Body.Email, Name: in.Body.Name, Password: in.Body.Password})
		if err != nil {
			return nil, err
		}
		if err := api.StartUserSession(ctx, sessions, res.userID, res.Team.ID); err != nil {
			return nil, err
		}
		return &acceptOutput{Body: res}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getInstance", Method: http.MethodGet, Path: "/instance", Tags: []string{"teams"},
		Summary: "Instance settings", Description: "Whether team creation is allowed; SMTP details only for the instance admin (the first user).",
		Errors: []int{http.StatusUnauthorized},
	}), func(ctx context.Context, _ *struct{}) (*instanceOutput, error) {
		i, err := svc.GetInstance(ctx)
		if err != nil {
			return nil, err
		}
		return &instanceOutput{Body: i}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateInstance", Method: http.MethodPatch, Path: "/instance", Tags: []string{"teams"},
		Summary:     "Change the instance settings",
		Description: "Instance admin only: allow team creation and the SMTP relay used to mail invites when a team has no email channel.",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity},
	}, policy.ScopeAdmin), func(ctx context.Context, in *updateInstanceInput) (*instanceOutput, error) {
		i, err := svc.UpdateInstance(ctx, InstancePatch{
			AllowTeamCreation: in.Body.AllowTeamCreation, SMTPHost: in.Body.SMTPHost, SMTPPort: in.Body.SMTPPort,
			SMTPTLS: in.Body.SMTPTLS, SMTPUsername: in.Body.SMTPUsername, SMTPPassword: in.Body.SMTPPassword,
			SMTPFrom: in.Body.SMTPFrom, SMTPInsecureSkipVerify: in.Body.SMTPInsecureSkipVerify,
		})
		if err != nil {
			return nil, err
		}
		return &instanceOutput{Body: i}, nil
	})
}
