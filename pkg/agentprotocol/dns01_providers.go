package agentprotocol

// Curated DNS-01 providers. Every Env name is what lego reads, checked against
// the catalog of the lego release Traefik vendors (TestCuratedFieldsExistInLegoCatalog):
// providers/dns/<code>/<code>.toml at LegoVersion, rendered at
// https://go-acme.github.io/lego/dns/<code>/.
//
// A field marked Required must be present; OneOf names alternative
// authentication methods, of which one must be complete. Lego reads each
// variable from the process environment of the Traefik container; the
// credential is never a flag or a file.

// Codes of the curated providers (the first four are in dns01.go).
const (
	DNSGandi      = "gandiv5"
	DNSOVH        = "ovh"
	DNSPorkbun    = "porkbun"
	DNSNamecheap  = "namecheap"
	DNSGoogle     = "gcloud"
	DNSAzure      = "azuredns"
	DNSDeSEC      = "desec"
	DNSLinode     = "linode"
	DNSVultr      = "vultr"
	DNSScaleway   = "scaleway"
	DNSBunny      = "bunny"
	DNSDNSimple   = "dnsimple"
	DNSIONOS      = "ionos"
	DNSGoDaddy    = "godaddy"
	DNSNetcup     = "netcup"
	dnsHelpToken  = "Write-only: sealed at rest, never shown again."
	dnsHelpPublic = "An identifier, not a secret: shown to administrators."
)

// DNSProviders is the curated list: providers with a form and credential help.
// Any other lego provider uses the generic form (LegoProviders).
var DNSProviders = []DNSProviderSpec{
	{
		Code: DNSCloudflare, Name: "Cloudflare", Docs: LegoDocsURL(DNSCloudflare),
		Help: "Use an API token with Zone:Zone:Read and Zone:DNS:Edit for the zones you want certificates for.",
		Fields: []DNSField{
			{Env: "CF_DNS_API_TOKEN", Label: "API token (DNS:Edit)", Help: "Token with Zone / DNS / Edit. Also needs Zone / Zone / Read unless you set the zone token below.", Secret: true, Required: true},
			{Env: "CF_ZONE_API_TOKEN", Label: "Zone token (Zone:Read)", Help: "Only when you split the permissions over two tokens.", Secret: true},
		},
	},
	{
		Code: DNSRoute53, Name: "Amazon Route 53", Docs: LegoDocsURL(DNSRoute53),
		Help: "An IAM user limited to route53:ChangeResourceRecordSets, ListResourceRecordSets, GetChange and ListHostedZonesByName.",
		Fields: []DNSField{
			{Env: "AWS_ACCESS_KEY_ID", Label: "Access key ID", Required: true},
			{Env: "AWS_SECRET_ACCESS_KEY", Label: "Secret access key", Secret: true, Required: true},
			{Env: "AWS_REGION", Label: "Region", Help: "Route 53 is global; us-east-1 works for every zone.", Required: true},
			{Env: "AWS_HOSTED_ZONE_ID", Label: "Hosted zone ID", Help: "Skips the lookup of the public hosted zone by name."},
		},
	},
	{
		Code: DNSHetzner, Name: "Hetzner", Docs: LegoDocsURL(DNSHetzner),
		Help: "A Hetzner Cloud API token with read and write access to the project that owns the zone.",
		Fields: []DNSField{
			{Env: "HETZNER_API_TOKEN", Label: "API token", Secret: true, Required: true},
		},
	},
	{
		Code: DNSDigitalOcean, Name: "DigitalOcean", Docs: LegoDocsURL(DNSDigitalOcean),
		Help: "A personal access token with write scope.",
		Fields: []DNSField{
			{Env: "DO_AUTH_TOKEN", Label: "Personal access token", Secret: true, Required: true},
		},
	},
	{
		Code: DNSGandi, Name: "Gandi", Docs: LegoDocsURL(DNSGandi),
		Help: "A Gandi Personal Access Token (LiveDNS) with access to the organization that owns the domain.",
		Fields: []DNSField{
			{Env: "GANDIV5_PERSONAL_ACCESS_TOKEN", Label: "Personal Access Token", Help: dnsHelpToken, Secret: true, Required: true},
		},
	},
	{
		Code: DNSOVH, Name: "OVHcloud", Docs: LegoDocsURL(DNSOVH),
		Help: "Either an application key with its secret and a consumer key, or an OAuth2 service account (client ID and secret), or an access token. Use one method only: lego refuses a mix.",
		OneOf: [][]string{
			{"OVH_APPLICATION_KEY", "OVH_APPLICATION_SECRET", "OVH_CONSUMER_KEY"},
			{"OVH_CLIENT_ID", "OVH_CLIENT_SECRET"},
			{"OVH_ACCESS_TOKEN"},
		},
		Fields: []DNSField{
			{Env: "OVH_ENDPOINT", Label: "API endpoint", Help: "ovh-eu (the default) or ovh-ca. " + dnsHelpPublic},
			{Env: "OVH_APPLICATION_KEY", Label: "Application key (application key method)", Secret: true},
			{Env: "OVH_APPLICATION_SECRET", Label: "Application secret (application key method)", Secret: true},
			{Env: "OVH_CONSUMER_KEY", Label: "Consumer key (application key method)", Secret: true},
			{Env: "OVH_CLIENT_ID", Label: "Client ID (OAuth2 method)", Help: dnsHelpPublic},
			{Env: "OVH_CLIENT_SECRET", Label: "Client secret (OAuth2 method)", Secret: true},
			{Env: "OVH_ACCESS_TOKEN", Label: "Access token (token method)", Secret: true},
		},
	},
	{
		Code: DNSPorkbun, Name: "Porkbun", Docs: LegoDocsURL(DNSPorkbun),
		Help: "An API key pair. Turn on API access for each domain in the Porkbun domain settings first.",
		Fields: []DNSField{
			{Env: "PORKBUN_API_KEY", Label: "API key", Secret: true, Required: true},
			{Env: "PORKBUN_SECRET_API_KEY", Label: "Secret API key", Secret: true, Required: true},
		},
	},
	{
		Code: DNSNamecheap, Name: "Namecheap", Docs: LegoDocsURL(DNSNamecheap),
		Help: "API access must be enabled on the account, and the server's public IP added to the API's allowed IP list.",
		Fields: []DNSField{
			{Env: "NAMECHEAP_API_USER", Label: "API user", Help: dnsHelpPublic, Required: true},
			{Env: "NAMECHEAP_API_KEY", Label: "API key", Secret: true, Required: true},
		},
	},
	{
		Code: DNSGoogle, Name: "Google Cloud DNS", Docs: LegoDocsURL(DNSGoogle),
		Help: "A service account key (JSON) with the DNS Administrator role on the project that holds the zone.",
		Fields: []DNSField{
			{Env: "GCE_SERVICE_ACCOUNT", Label: "Service account key (JSON)", Help: "Paste the whole key file; it is stored on one line.", Secret: true, Required: true},
			{Env: "GCE_PROJECT", Label: "Project ID", Help: "Overrides the project named in the key. " + dnsHelpPublic},
			{Env: "GCE_ZONE_ID", Label: "Managed zone ID", Help: "Skips the automatic zone detection. " + dnsHelpPublic},
		},
	},
	{
		Code: DNSAzure, Name: "Azure DNS", Docs: LegoDocsURL(DNSAzure),
		Help: "A service principal (app registration) with the DNS Zone Contributor role on the zone.",
		Fields: []DNSField{
			{Env: "AZURE_CLIENT_ID", Label: "Client ID (application ID)", Help: dnsHelpPublic, Required: true},
			{Env: "AZURE_CLIENT_SECRET", Label: "Client secret", Secret: true, Required: true},
			{Env: "AZURE_TENANT_ID", Label: "Tenant ID", Help: dnsHelpPublic, Required: true},
			{Env: "AZURE_SUBSCRIPTION_ID", Label: "Subscription ID", Help: "With the resource group, limits the zone search to one place. " + dnsHelpPublic},
			{Env: "AZURE_RESOURCE_GROUP", Label: "Resource group", Help: dnsHelpPublic},
		},
	},
	{
		Code: DNSDeSEC, Name: "deSEC", Docs: LegoDocsURL(DNSDeSEC),
		Help: "A token from the deSEC token management page.",
		Fields: []DNSField{
			{Env: "DESEC_TOKEN", Label: "Token", Help: dnsHelpToken, Secret: true, Required: true},
		},
	},
	{
		Code: DNSLinode, Name: "Linode", Docs: LegoDocsURL(DNSLinode),
		Help: "A personal access token with read/write access to Domains.",
		Fields: []DNSField{
			{Env: "LINODE_TOKEN", Label: "Personal access token", Help: dnsHelpToken, Secret: true, Required: true},
		},
	},
	{
		Code: DNSVultr, Name: "Vultr", Docs: LegoDocsURL(DNSVultr),
		Help: "An API key, with the server's public IP allowed in the API access control list.",
		Fields: []DNSField{
			{Env: "VULTR_API_KEY", Label: "API key", Help: dnsHelpToken, Secret: true, Required: true},
		},
	},
	{
		Code: DNSScaleway, Name: "Scaleway", Docs: LegoDocsURL(DNSScaleway),
		Help: "An API key whose IAM policy allows DomainsDNSFullAccess (or the narrower DNS write permission) in the project.",
		Fields: []DNSField{
			{Env: "SCW_SECRET_KEY", Label: "Secret key", Help: dnsHelpToken, Secret: true, Required: true},
			{Env: "SCW_ACCESS_KEY", Label: "Access key", Help: dnsHelpPublic},
			{Env: "SCW_PROJECT_ID", Label: "Project ID", Help: dnsHelpPublic},
		},
	},
	{
		Code: DNSBunny, Name: "Bunny", Docs: LegoDocsURL(DNSBunny),
		Help: "The account API key from the bunny.net account settings.",
		Fields: []DNSField{
			{Env: "BUNNY_API_KEY", Label: "API key", Help: dnsHelpToken, Secret: true, Required: true},
		},
	},
	{
		Code: DNSDNSimple, Name: "DNSimple", Docs: LegoDocsURL(DNSDNSimple),
		Help: "An account API token (a user token is rejected).",
		Fields: []DNSField{
			{Env: "DNSIMPLE_OAUTH_TOKEN", Label: "Account API token", Help: dnsHelpToken, Secret: true, Required: true},
		},
	},
	{
		Code: DNSIONOS, Name: "IONOS", Docs: LegoDocsURL(DNSIONOS),
		Help: "An API key of the IONOS Hosting DNS API, written <prefix>.<secret>.",
		Fields: []DNSField{
			{Env: "IONOS_API_KEY", Label: "API key (prefix.secret)", Help: dnsHelpToken, Secret: true, Required: true},
		},
	},
	{
		Code: DNSGoDaddy, Name: "GoDaddy", Docs: LegoDocsURL(DNSGoDaddy),
		Help: "A production API key and secret. GoDaddy limits the DNS API to accounts with enough domains; see the docs.",
		Fields: []DNSField{
			{Env: "GODADDY_API_KEY", Label: "API key", Secret: true, Required: true},
			{Env: "GODADDY_API_SECRET", Label: "API secret", Secret: true, Required: true},
		},
	},
	{
		Code: DNSNetcup, Name: "Netcup", Docs: LegoDocsURL(DNSNetcup),
		Help: "Customer number, plus an API key and API password created in the customer control panel.",
		Fields: []DNSField{
			{Env: "NETCUP_CUSTOMER_NUMBER", Label: "Customer number", Help: dnsHelpPublic, Required: true},
			{Env: "NETCUP_API_KEY", Label: "API key", Secret: true, Required: true},
			{Env: "NETCUP_API_PASSWORD", Label: "API password", Secret: true, Required: true},
		},
	},
}
