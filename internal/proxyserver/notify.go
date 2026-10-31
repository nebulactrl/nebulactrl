package proxyserver

import (
	"context"
	"fmt"
	"strconv"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
)

// notifyExpiring announces a certificate that just crossed an expiry
// threshold (30 days, 7 days) or expired (threshold -1). Each threshold is
// announced once: the caller only gets here when the audit event was new.
func notifyExpiring(ctx context.Context, em notifications.Emitter, c db.ListCertificatesForExpiryRow, days, threshold int) {
	if em == nil {
		return
	}
	serverID := c.ServerID
	ev := notifications.Event{
		Type: notifications.TypeCertExpiring, ServerID: &serverID, WorkspaceID: c.WorkspaceID,
		Fields: map[string]string{"Certificate": c.Name, "Expires": c.NotAfter.UTC().Format("2006-01-02")},
	}
	switch {
	case threshold < 0:
		ev.Severity = notifications.SeverityError
		ev.Title = "Certificate " + c.Name + " has expired"
		ev.Body = "Visitors get certificate errors until a new certificate is uploaded."
	case threshold <= expiryCriticalDays:
		ev.Severity = notifications.SeverityError
		ev.Title = "Certificate " + c.Name + " expires in " + strconv.Itoa(max(days, 0)) + " days"
		ev.Body = fmt.Sprintf("It expires in %d days. Upload a renewed certificate before then.", max(days, 0))
	default:
		ev.Severity = notifications.SeverityWarning
		ev.Title = "Certificate " + c.Name + " expires in " + strconv.Itoa(max(days, 0)) + " days"
		ev.Body = fmt.Sprintf("It expires in %d days. Upload a renewed certificate soon.", max(days, 0))
	}
	notifications.Emit(ctx, em, ev)
}
