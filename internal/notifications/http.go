package notifications

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type channelIDInput struct {
	ID uuid.UUID `path:"channelId"`
}

type channelOutput struct{ Body ChannelView }

type channelListOutput struct {
	Body struct {
		Items []ChannelView `json:"items"`
	}
}

type eventTypesOutput struct {
	Body struct {
		Items []ChannelEventType `json:"items"`
	}
}

type createChannelInput struct {
	Body struct {
		Name    string              `json:"name" minLength:"1" maxLength:"80"`
		Kind    string              `json:"kind" enum:"email,slack,discord,telegram,webhook"`
		Enabled *bool               `json:"enabled,omitempty" doc:"Default true."`
		Config  ChannelConfig       `json:"config,omitempty"`
		Secrets ChannelSecretsInput `json:"secrets,omitempty"`
		Rule    *ChannelRuleInput   `json:"rule,omitempty" doc:"Default: failures and alerts on, successes off, the whole workspace, every severity."`
	}
}

type createChannelOutput struct {
	Body struct {
		Channel       ChannelView `json:"channel"`
		SigningSecret string      `json:"signingSecret,omitempty" doc:"Webhook channels: the generated signing secret. Shown only in this response; store it now. Rotate it to get a new one."`
	}
}

type patchChannelInput struct {
	ID   uuid.UUID `path:"channelId"`
	Body struct {
		Name    *string              `json:"name,omitempty" minLength:"1" maxLength:"80"`
		Enabled *bool                `json:"enabled,omitempty"`
		Config  *ChannelConfig       `json:"config,omitempty" doc:"Replaces the non-secret settings."`
		Secrets *ChannelSecretsInput `json:"secrets,omitempty" doc:"Fields left empty keep the stored credential."`
		Rule    *ChannelRuleInput    `json:"rule,omitempty" doc:"Replaces the subscription rule."`
	}
}

type deliveryOutput struct{ Body ChannelDelivery }

type rotateOutput struct {
	Body struct {
		SigningSecret string `json:"signingSecret" doc:"The new signing secret. Shown only in this response."`
	}
}

type listDeliveriesInput struct {
	ID     uuid.UUID `path:"channelId"`
	Status string    `query:"status" enum:"pending,retrying,delivered,failed" doc:"Only deliveries in this status."`
	api.PageQuery
}

type deliveryPageOutput struct{ Body ChannelDeliveryPage }

// Register attaches the notification endpoints. Everything needs the admin scope:
// channels hold credentials and the log shows events of every project.
func Register(h huma.API, svc *Service) {
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}
	tag := []string{"notifications"}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listNotificationEventTypes", Method: http.MethodGet, Path: "/notifications/event-types", Tags: tag,
		Summary: "List notification event types", Description: "The events a channel can subscribe to, grouped, with their default severity and whether new channels subscribe by default.",
		Errors: errs[:2],
	}, policy.ScopeAdmin), func(ctx context.Context, _ *struct{}) (*eventTypesOutput, error) {
		items, err := svc.EventTypes(ctx)
		if err != nil {
			return nil, err
		}
		out := &eventTypesOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listNotificationChannels", Method: http.MethodGet, Path: "/notifications/channels", Tags: tag,
		Summary: "List notification channels", Description: "Credentials are never returned; `secrets` holds masked hints of the ones that are set.",
		Errors: errs[:2],
	}, policy.ScopeAdmin), func(ctx context.Context, _ *struct{}) (*channelListOutput, error) {
		items, err := svc.ListChannels(ctx)
		if err != nil {
			return nil, err
		}
		out := &channelListOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "createNotificationChannel", Method: http.MethodPost, Path: "/notifications/channels", Tags: tag,
		Summary: "Add a notification channel", DefaultStatus: http.StatusCreated,
		Description: "Kinds: `email` (SMTP: host, port, tls implicit|starttls|none, username, from, to; password in `secrets`), `slack` and `discord` (webhook URL in `secrets.url`), " +
			"`telegram` (`config.chatId`, bot token in `secrets.botToken`) and `webhook` (URL in `secrets.url`; every request is signed with HMAC-SHA256 in the `X-NebulaCtrl-Signature` header). " +
			"A webhook's signing secret is generated when omitted and returned once. Not idempotent: the response can carry that secret, so a repeated name answers 409 `channel_conflict` instead of replaying it. Requires the admin scope.",
		Errors: append(errs[:2], http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *createChannelInput) (*createChannelOutput, error) {
		res, err := svc.CreateChannel(ctx, ChannelInput{
			Name: in.Body.Name, Kind: in.Body.Kind, Enabled: in.Body.Enabled, Config: in.Body.Config, Secrets: in.Body.Secrets, Rule: in.Body.Rule,
		})
		if err != nil {
			return nil, err
		}
		out := &createChannelOutput{}
		out.Body.Channel, out.Body.SigningSecret = res.Channel, res.SigningSecret
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getNotificationChannel", Method: http.MethodGet, Path: "/notifications/channels/{channelId}", Tags: tag,
		Summary: "Get a notification channel", Errors: errs,
	}, policy.ScopeAdmin), func(ctx context.Context, in *channelIDInput) (*channelOutput, error) {
		v, err := svc.GetChannel(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &channelOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateNotificationChannel", Method: http.MethodPatch, Path: "/notifications/channels/{channelId}", Tags: tag,
		Summary: "Update a notification channel", Description: "Omitted fields stay as they are; empty credentials keep the stored ones. The kind can not change.",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *patchChannelInput) (*channelOutput, error) {
		v, err := svc.UpdateChannel(ctx, in.ID, ChannelPatch{
			Name: in.Body.Name, Enabled: in.Body.Enabled, Config: in.Body.Config, Secrets: in.Body.Secrets, Rule: in.Body.Rule,
		})
		if err != nil {
			return nil, err
		}
		return &channelOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteNotificationChannel", Method: http.MethodDelete, Path: "/notifications/channels/{channelId}", Tags: tag,
		Summary: "Delete a notification channel", Description: "Deletes the channel and its delivery log.", DefaultStatus: http.StatusNoContent, Errors: errs,
	}, policy.ScopeAdmin), func(ctx context.Context, in *channelIDInput) (*struct{}, error) {
		return nil, svc.DeleteChannel(ctx, in.ID)
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "testNotificationChannel", Method: http.MethodPost, Path: "/notifications/channels/{channelId}/test", Tags: tag,
		Summary: "Send a test notification",
		Description: "Sends a test message through the channel right now and returns the delivery that records the result (status `delivered` or `failed`, with the error and response code). " +
			"The test is not queued, retried, rate limited or deduplicated, and works on a disabled channel. It also updates the channel's `test` state.",
		Errors: errs,
	}, policy.ScopeAdmin), func(ctx context.Context, in *channelIDInput) (*deliveryOutput, error) {
		v, err := svc.TestChannel(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &deliveryOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "rotateNotificationChannelSecret", Method: http.MethodPost, Path: "/notifications/channels/{channelId}/rotate-secret", Tags: tag,
		Summary: "Rotate a webhook signing secret", Description: "Webhook channels only. The new secret is returned once; deliveries are signed with it from now on.",
		Errors: append(errs, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *channelIDInput) (*rotateOutput, error) {
		secret, err := svc.RotateSecret(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		out := &rotateOutput{}
		out.Body.SigningSecret = secret
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listNotificationDeliveries", Method: http.MethodGet, Path: "/notifications/channels/{channelId}/deliveries", Tags: tag,
		Summary: "List a channel's deliveries",
		Description: "The delivery log, newest first, kept for 30 days: status (`pending`, `retrying`, `delivered`, `failed`), attempts, the last error and the response code. " +
			"Errors never contain credentials.",
		Errors: errs,
	}, policy.ScopeAdmin), func(ctx context.Context, in *listDeliveriesInput) (*deliveryPageOutput, error) {
		page, err := svc.ListDeliveries(ctx, in.ID, in.Status, in.Cursor, in.Size())
		if err != nil {
			return nil, err
		}
		return &deliveryPageOutput{Body: page}, nil
	})
}
