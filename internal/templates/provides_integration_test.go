package templates_test

import (
	"io/fs"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/templates"
	catalogfs "github.com/nebulactrl/nebulactrl/templates"
)

func proposalByKey(ps []services.ProposedVariable) map[string]services.ProposedVariable {
	out := map[string]services.ProposedVariable{}
	for _, p := range ps {
		out[p.Key] = p
	}
	return out
}

// Linking a service to an installed Bucket proposes the S3 settings the
// template declares in `provides`, with references rewritten to the bucket's
// real slug, next to the generic host and port rows.
func TestLinkToBucketProposesS3Variables(t *testing.T) {
	f := newTfx(t)
	body, err := fs.ReadFile(catalogfs.FS, "catalog/bucket.yaml")
	require.NoError(t, err)
	svc := f.service(catalogOf(t, map[string]string{"bucket": string(body), "demo": demoYAML(demoV1())}))
	f.svcs.SetLinkProvider(svc)

	bucket := f.serviceByKey(f.install(svc, "bucket", templates.TemplateInstallRequest{}), "bucket")
	consumer := f.serviceByKey(f.install(svc, "demo", templates.TemplateInstallRequest{}), "app")
	require.Equal(t, "bucket", bucket.Slug)

	want := map[string]struct {
		value  string
		secret bool
	}{
		"AWS_ENDPOINT_URL":        {value: "http://${{bucket.NEBULA_PRIVATE_HOST}}:${{bucket.PORT}}"},
		"AWS_ACCESS_KEY_ID":       {value: "${{bucket.GARAGE_DEFAULT_ACCESS_KEY}}"},
		"AWS_SECRET_ACCESS_KEY":   {value: "${{bucket.GARAGE_DEFAULT_SECRET_KEY}}", secret: true},
		"AWS_REGION":              {value: "garage"},
		"S3_BUCKET":               {value: "${{bucket.GARAGE_DEFAULT_BUCKET}}"},
		"AWS_S3_FORCE_PATH_STYLE": {value: "true"},
	}

	pv, err := f.svcs.PreviewLink(f.ctx, f.env.ID, consumer.ID, bucket.ID)
	require.NoError(t, err)
	got := proposalByKey(pv.ProposedVariables)
	for k, w := range want {
		p, ok := got[k]
		require.True(t, ok, "preview misses %s", k)
		require.Equal(t, w.value, p.Value, k)
		require.Equal(t, w.secret, p.Secret, k)
		require.True(t, p.Recommended, k)
		require.Equal(t, "new", p.Status, k)
		require.NotEmpty(t, p.Description, k)
	}
	require.Contains(t, got, "BUCKET_HOST", "the generic rows stay")
	require.Contains(t, got, "BUCKET_PORT")

	// Creating the link returns the same rows the consumer does not hold yet.
	res, err := f.svcs.CreateLink(f.ctx, f.env.ID, consumer.ID, bucket.ID)
	require.NoError(t, err)
	created := proposalByKey(res.ProposedVariables)
	for k, w := range want {
		require.Equal(t, w.value, created[k].Value, k)
	}

	// The link transaction path proposes the same rows.
	f.exec("DELETE FROM service_links WHERE environment_id = $1", f.env.ID)
	env, err := services.LoadEnvironment(f.ctx, f.q, policy.ScopeProjectsWrite, f.env.ID)
	require.NoError(t, err)
	var viaTx services.LinkResult
	require.NoError(t, db.InTx(f.ctx, f.pool, func(tx pgx.Tx) error {
		var err error
		viaTx, _, err = f.svcs.CreateLinkTx(f.ctx, tx, env, consumer.ID, bucket.ID)
		return err
	}))
	require.Contains(t, proposalByKey(viaTx.ProposedVariables), "AWS_ENDPOINT_URL")

	// A key the consumer already holds is not proposed again by the link.
	f.exec("DELETE FROM service_links WHERE environment_id = $1", f.env.ID)
	require.NoError(t, db.InTx(f.ctx, f.pool, func(tx pgx.Tx) error {
		_, err := f.vars.UpsertTx(f.ctx, tx, consumer.ID, "S3_BUCKET", "mine", false)
		return err
	}))
	pv, err = f.svcs.PreviewLink(f.ctx, f.env.ID, consumer.ID, bucket.ID)
	require.NoError(t, err)
	require.Equal(t, "replaces", proposalByKey(pv.ProposedVariables)["S3_BUCKET"].Status)
}

// A service that is not a template's service (or a published-port route of
// one), or a template service without `provides`, gets no extra rows.
func TestLinkProposalsOnlyForTemplateServicesWithProvides(t *testing.T) {
	f := newTfx(t)
	svc := f.service(catalogOf(t, map[string]string{"demo": demoYAML(demoV1())}))
	app := f.serviceByKey(f.install(svc, "demo", templates.TemplateInstallRequest{}), "app")
	require.NotNil(t, app.TemplateInstanceID)

	got, err := svc.LinkProposals(f.ctx, f.q, app)
	require.NoError(t, err)
	require.Empty(t, got, "the demo app declares no provides")

	route := app
	route.TemplateKey = new("port:app:udp")
	got, err = svc.LinkProposals(f.ctx, f.q, route)
	require.NoError(t, err)
	require.Nil(t, got)

	loose := app
	loose.TemplateInstanceID = nil
	got, err = svc.LinkProposals(f.ctx, f.q, loose)
	require.NoError(t, err)
	require.Nil(t, got)
}
