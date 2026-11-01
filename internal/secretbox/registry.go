package secretbox

import "strings"

// Column identifies one sealed database column. Every `*_enc` column is a
// registry entry below; there are no ad-hoc contexts. The registry is the one
// list `nebulactrl-server rekey` walks, and a test fails when a migration adds a
// bytea `*_enc` column that is not listed.
type Column struct {
	Table  string
	Column string
	// RowKey is a SQL expression over the table's columns that yields the row
	// id string callers pass as rowID. rekey evaluates it to rebuild the AAD.
	// It MUST match what the Go call sites pass (see RowID).
	RowKey string
}

// String is "table.column".
func (c Column) String() string { return c.Table + "." + c.Column }

// InstanceRow is the row id of the singleton instance_* tables.
const InstanceRow = "instance"

// RowID joins the parts of a composite row key the way the registry's RowKey
// expressions do (parts separated by "/").
func RowID(parts ...string) string { return strings.Join(parts, "/") }

// All returns every registered sealed column.
func All() []Column { return append([]Column(nil), all...) }

var all []Column

func reg(table, column, rowKey string) Column {
	c := Column{Table: table, Column: column, RowKey: rowKey}
	all = append(all, c)
	return c
}

const (
	keyID        = "id::text"
	keyWorkspace = "workspace_id::text"
	keyService   = "service_id::text"
)

// The sealed columns. Row ids: the row's uuid unless noted.
var (
	// services
	ServicesGitToken     = reg("services", "git_token_enc", keyID)
	ServicesRegistryAuth = reg("services", "registry_auth_enc", keyID)

	// Variables. service_variables is keyed by (service_id, key): RowID(serviceID, key).
	ServiceVariablesValue    = reg("service_variables", "value_enc", "service_id::text || '/' || key")
	ServiceVariablesTemplate = reg("service_variables", "template_enc", "service_id::text || '/' || key")
	SharedVariablesValue     = reg("shared_variables", "value_enc", keyID)
	SharedVariablesTemplate  = reg("shared_variables", "template_enc", keyID)
	// variable_versions is append-only: RowID(ownerKind, ownerID, key, version).
	VariableVersionsValue = reg("variable_versions", "value_enc", "owner_kind || '/' || owner_id::text || '/' || key || '/' || version::text")

	// ReleasesEnv is the sealed EnvSnapshot of a release.
	ReleasesEnv = reg("releases", "env_enc", keyID)

	// Service credentials. service_ssh_keys is keyed by service_id.
	ServiceSSHKeysPrivateKey = reg("service_ssh_keys", "private_key_enc", keyService)
	RegistriesPushAuth       = reg("registries", "push_auth_enc", keyID)
	RegistriesPullAuth       = reg("registries", "pull_auth_enc", keyID)

	// TLS and proxy. domain_auth_users is keyed by RowID(domainID, username).
	CertificatesKey       = reg("certificates", "key_enc", keyID)
	DomainAuthUsersHash   = reg("domain_auth_users", "hash_enc", "domain_id::text || '/' || username")
	DNSCredentialsSecrets = reg("dns_credentials", "secrets_enc", keyID)

	// Integrations. cloudflare_connections, tailnet_integrations and
	// geo_credentials are keyed by workspace_id.
	CloudflareConnectionsToken    = reg("cloudflare_connections", "token_enc", keyWorkspace)
	GitConnectionsCredentials     = reg("git_connections", "credentials_enc", keyID)
	GitConnectionsWebhookSecret   = reg("git_connections", "webhook_secret_enc", keyID)
	TailnetIntegrationsSecret     = reg("tailnet_integrations", "secret_enc", keyWorkspace)
	SecretIntegrationsCredentials = reg("secret_integrations", "credentials_enc", keyID)
	NotificationChannelsSecrets   = reg("notification_channels", "secrets_enc", keyID)
	LogDrainsSecrets              = reg("log_drains", "secrets_enc", keyID)
	PlatformServicesSecrets       = reg("platform_services", "secrets_enc", keyID)
	GeoCredentialsMaxmindKey      = reg("geo_credentials", "maxmind_key_enc", keyWorkspace)

	// Instance services. instance_services is keyed by kind; the slice secret by id.
	InstanceServicesSecrets = reg("instance_services", "secrets_enc", "kind")
	TeamSliceSecrets        = reg("team_service_slices", "secret_enc", keyID)

	// Backups. instance_backup_settings is the singleton (InstanceRow);
	// volume_repos is keyed by RowID(serviceID, destinationID).
	BackupDestinationsAccessKey = reg("backup_destinations", "access_key_enc", keyID)
	BackupDestinationsSecretKey = reg("backup_destinations", "secret_key_enc", keyID)
	InstanceBackupAccessKey     = reg("instance_backup_settings", "access_key_enc", "'"+InstanceRow+"'")
	InstanceBackupSecretKey     = reg("instance_backup_settings", "secret_key_enc", "'"+InstanceRow+"'")
	VolumeReposPassword         = reg("volume_repos", "password_enc", "service_id::text || '/' || destination_id::text")

	// Other. instance_settings is the singleton (InstanceRow); user_totp is
	// keyed by user_id.
	AuthProvidersClientSecret    = reg("auth_providers", "client_secret_enc", keyID)
	InstanceSettingsSMTPPassword = reg("instance_settings", "smtp_password_enc", "'"+InstanceRow+"'")
	DatabaseRotationsNewPassword = reg("database_rotations", "new_password_enc", keyID)
	UserTOTPSecret               = reg("user_totp", "secret_enc", "user_id::text")
)
