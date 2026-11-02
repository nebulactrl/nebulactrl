package backup

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestDumpForBuildsEngineCommands(t *testing.T) {
	pw := map[string]string{agentprotocol.SecretDBPassword: "pw-123"}
	tests := []struct {
		name   string
		target agentprotocol.BackupTarget
		want   dumpSpec
	}{
		{
			name:   "mariadb uses mariadb-dump and its own format",
			target: agentprotocol.BackupTarget{Engine: "mariadb", Database: "app", User: "root"},
			want: dumpSpec{
				tool: "mariadb-dump", format: agentprotocol.FormatMariaDBGz, gzip: true,
				argv: []string{"mariadb-dump", "--single-transaction", "--quick", "--no-tablespaces", "--routines", "--events", "--triggers", "-u", "root", "app"},
				env:  []string{"MYSQL_PWD=pw-123"},
			},
		},
		{
			name:   "mysql uses mysqldump",
			target: agentprotocol.BackupTarget{Engine: "mysql", Database: "app", User: "root"},
			want: dumpSpec{
				tool: "mysqldump", format: agentprotocol.FormatMySQLGz, gzip: true,
				argv: []string{"mysqldump", "--single-transaction", "--quick", "--no-tablespaces", "--routines", "--events", "--triggers", "-u", "root", "app"},
				env:  []string{"MYSQL_PWD=pw-123"},
			},
		},
		{
			name:   "mongodb carries credentials in the URI and is not gzipped by the agent",
			target: agentprotocol.BackupTarget{Engine: "mongodb", User: "root"},
			want: dumpSpec{
				tool: "mongodump", format: agentprotocol.FormatMongoArchive,
				argv: []string{"mongodump", "--archive", "--gzip", "--quiet", "--uri", "mongodb://root:pw-123@127.0.0.1:27017/?authSource=admin"},
			},
		},
		{
			name:   "valkey default user needs no --user and passes the password by env",
			target: agentprotocol.BackupTarget{Engine: "valkey", User: "default"},
			want: dumpSpec{
				tool: "valkey-cli", format: agentprotocol.FormatRDB,
				argv: []string{"valkey-cli", "--rdb", "-"},
				env:  []string{"REDISCLI_AUTH=pw-123", "VALKEYCLI_AUTH=pw-123"},
			},
		},
		{
			name:   "redis with an ACL user",
			target: agentprotocol.BackupTarget{Engine: "redis", User: "backup"},
			want: dumpSpec{
				tool: "redis-cli", format: agentprotocol.FormatRDB,
				argv: []string{"redis-cli", "--user", "backup", "--rdb", "-"},
				env:  []string{"REDISCLI_AUTH=pw-123", "VALKEYCLI_AUTH=pw-123"},
			},
		},
		{
			name:   "redis without a user",
			target: agentprotocol.BackupTarget{Engine: "redis"},
			want: dumpSpec{
				tool: "redis-cli", format: agentprotocol.FormatRDB,
				argv: []string{"redis-cli", "--rdb", "-"},
				env:  []string{"REDISCLI_AUTH=pw-123", "VALKEYCLI_AUTH=pw-123"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := dumpFor(tc.target, pw)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestDumpForRedisWithoutPasswordSetsNoAuthEnv(t *testing.T) {
	got, err := dumpFor(agentprotocol.BackupTarget{Engine: "valkey"}, nil)
	require.NoError(t, err)
	require.Nil(t, got.env)
}

func TestDumpForRejectsUnsafeInput(t *testing.T) {
	tests := []struct {
		name    string
		target  agentprotocol.BackupTarget
		wantErr string
	}{
		{"postgres database with option injection", agentprotocol.BackupTarget{Engine: "postgres", Database: "--help", User: "u"}, `invalid database "--help"`},
		{"postgres user with shell metacharacters", agentprotocol.BackupTarget{Engine: "postgres", Database: "d", User: "u;rm"}, `invalid user "u;rm"`},
		{"mysql database empty", agentprotocol.BackupTarget{Engine: "mysql", Database: "", User: "u"}, `invalid database ""`},
		{"mariadb user too long", agentprotocol.BackupTarget{Engine: "mariadb", Database: "d", User: strings.Repeat("a", 64)}, `invalid user "` + strings.Repeat("a", 64) + `"`},
		{"mongodb user with URI metacharacters", agentprotocol.BackupTarget{Engine: "mongodb", User: "a@b"}, `invalid user "a@b"`},
		{"redis acl user with leading dash", agentprotocol.BackupTarget{Engine: "redis", User: "-x"}, `invalid user "-x"`},
		{"unsupported engine", agentprotocol.BackupTarget{Engine: "cassandra"}, `backups are not supported for engine "cassandra"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := dumpFor(tc.target, nil)
			require.EqualError(t, err, tc.wantErr)
		})
	}
}

func TestRestoreForBuildsEngineCommands(t *testing.T) {
	pw := map[string]string{agentprotocol.SecretDBPassword: "pw-123"}
	pg := agentprotocol.DBRestorePayload{
		RestoreID: "0a1b2c3d-1111-2222-3333-444455556666",
		Target:    agentprotocol.BackupTarget{Engine: "postgres", Database: "app", User: "u"},
		Format:    agentprotocol.FormatPGCustom,
	}
	got, err := restoreFor(pg, pw)
	require.NoError(t, err)
	require.Equal(t, restoreSpec{
		tool: "pg_restore", scratch: true,
		argv: []string{"pg_restore", "--no-owner", "--no-acl", "--exit-on-error", "--username", "u", "--dbname", "nc_restore_0a1b2c3d1111"},
		env:  []string{"PGPASSWORD=pw-123"},
	}, got)

	// mysql and mariadb dumps are interchangeable
	for _, format := range []string{agentprotocol.FormatMySQLGz, agentprotocol.FormatMariaDBGz} {
		got, err = restoreFor(agentprotocol.DBRestorePayload{
			Target: agentprotocol.BackupTarget{Engine: "mariadb", Database: "app", User: "root"}, Format: format,
		}, pw)
		require.NoError(t, err, format)
		require.Equal(t, restoreSpec{
			tool: "mariadb", gunzip: true,
			argv: []string{"mariadb", "--binary-mode", "-u", "root", "app"},
			env:  []string{"MYSQL_PWD=pw-123"},
		}, got, format)
	}
	got, err = restoreFor(agentprotocol.DBRestorePayload{
		Target: agentprotocol.BackupTarget{Engine: "mysql", Database: "app", User: "root"}, Format: agentprotocol.FormatMariaDBGz,
	}, pw)
	require.NoError(t, err)
	require.Equal(t, "mysql", got.tool)

	mongo := agentprotocol.DBRestorePayload{Target: agentprotocol.BackupTarget{Engine: "mongodb", User: "root"}, Format: agentprotocol.FormatMongoArchive}
	got, err = restoreFor(mongo, pw)
	require.NoError(t, err)
	require.Equal(t, []string{"mongorestore", "--archive", "--gzip", "--quiet", "--uri", "mongodb://root:pw-123@127.0.0.1:27017/?authSource=admin"}, got.argv)
	mongo.Clean = true
	got, err = restoreFor(mongo, pw)
	require.NoError(t, err)
	require.Equal(t, "--drop", got.argv[len(got.argv)-1])
}

func TestRestoreForRejectsMismatchesAndUnsafeInput(t *testing.T) {
	tests := []struct {
		name    string
		p       agentprotocol.DBRestorePayload
		wantErr string
	}{
		{"postgres from mysql dump", agentprotocol.DBRestorePayload{Target: agentprotocol.BackupTarget{Engine: "postgres", Database: "d", User: "u"}, Format: agentprotocol.FormatMySQLGz}, "a mysql_sql_gz dump cannot be restored into postgres"},
		{"postgres database invalid", agentprotocol.DBRestorePayload{Target: agentprotocol.BackupTarget{Engine: "postgres", Database: "-d", User: "u"}, Format: agentprotocol.FormatPGCustom}, `invalid database "-d"`},
		{"postgres user invalid", agentprotocol.DBRestorePayload{Target: agentprotocol.BackupTarget{Engine: "postgres", Database: "d", User: "a b"}, Format: agentprotocol.FormatPGCustom}, `invalid user "a b"`},
		{"mysql from pg dump", agentprotocol.DBRestorePayload{Target: agentprotocol.BackupTarget{Engine: "mysql", Database: "d", User: "u"}, Format: agentprotocol.FormatPGCustom}, "a pg_custom dump cannot be restored into mysql (want mysql_sql_gz)"},
		{"mariadb from pg dump names its own format", agentprotocol.DBRestorePayload{Target: agentprotocol.BackupTarget{Engine: "mariadb", Database: "d", User: "u"}, Format: agentprotocol.FormatPGCustom}, "a pg_custom dump cannot be restored into mariadb (want mariadb_sql_gz)"},
		{"mysql database invalid", agentprotocol.DBRestorePayload{Target: agentprotocol.BackupTarget{Engine: "mysql", Database: "d;", User: "u"}, Format: agentprotocol.FormatMySQLGz}, `invalid database "d;"`},
		{"mysql user invalid", agentprotocol.DBRestorePayload{Target: agentprotocol.BackupTarget{Engine: "mysql", Database: "d", User: ""}, Format: agentprotocol.FormatMySQLGz}, `invalid user ""`},
		{"mongodb from rdb", agentprotocol.DBRestorePayload{Target: agentprotocol.BackupTarget{Engine: "mongodb", User: "u"}, Format: agentprotocol.FormatRDB}, "a rdb dump cannot be restored into mongodb"},
		{"mongodb user invalid", agentprotocol.DBRestorePayload{Target: agentprotocol.BackupTarget{Engine: "mongodb", User: "u/x"}, Format: agentprotocol.FormatMongoArchive}, `invalid user "u/x"`},
		{"valkey restore unsupported", agentprotocol.DBRestorePayload{Target: agentprotocol.BackupTarget{Engine: "valkey"}, Format: agentprotocol.FormatRDB}, "restoring valkey dumps is not supported"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := restoreFor(tc.p, nil)
			require.EqualError(t, err, tc.wantErr)
		})
	}
}

func TestMongoURIEscapesCredentials(t *testing.T) {
	require.Equal(t, "mongodb://root:p%40ss%3Aw%2Frd@127.0.0.1:27017/?authSource=admin", mongoURI("root", "p@ss:w/rd"))
	require.Equal(t, "mongodb://root@127.0.0.1:27017/?authSource=admin", mongoURI("root", ""))
}

func TestCheckIdent(t *testing.T) {
	for _, ok := range []string{"a", "_x", "app-db", "app.db", "a$b", "9lives", strings.Repeat("a", 63)} {
		require.NoError(t, checkIdent("database", ok), ok)
	}
	for _, bad := range []string{"", "-x", ".x", "a b", "a;b", "a'b", "a/b", "a\nb", strings.Repeat("a", 64)} {
		require.EqualError(t, checkIdent("database", bad), fmt.Sprintf("invalid database %q", bad), bad)
	}
}

func TestIsNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"typed NoSuchKey", &s3types.NoSuchKey{}, true},
		{"typed NotFound (HEAD)", &s3types.NotFound{}, true},
		{"wrapped typed error", fmt.Errorf("head k: %w", &s3types.NotFound{}), true},
		{"API error code NoSuchKey", &smithy.GenericAPIError{Code: "NoSuchKey"}, true},
		{"API error code NotFound", &smithy.GenericAPIError{Code: "NotFound"}, true},
		{"API error code 404", &smithy.GenericAPIError{Code: "404"}, true},
		{"status text from a non-AWS server", errors.New("operation error S3: HeadObject, https response error StatusCode: 404, RequestID: x"), true},
		{"access denied is not a miss", &smithy.GenericAPIError{Code: "AccessDenied"}, false},
		{"other status text", errors.New("StatusCode: 500"), false},
		{"plain error", errors.New("boom"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isNotFound(tc.err))
		})
	}
}

func TestNewStoreFactory(t *testing.T) {
	root := t.TempDir()
	factory := NewStoreFactory(root)

	st, err := factory(agentprotocol.BackupDestination{Kind: agentprotocol.DestLocal, LocalDir: "sub"}, nil)
	require.NoError(t, err)
	ls, ok := st.(*LocalStore)
	require.True(t, ok, "a local destination yields a LocalStore")
	require.Equal(t, root+"/sub", ls.root)

	_, err = factory(agentprotocol.BackupDestination{Kind: agentprotocol.DestLocal, LocalDir: "../escape"}, nil)
	require.ErrorContains(t, err, "invalid local directory")

	st, err = factory(agentprotocol.BackupDestination{Kind: agentprotocol.DestS3, Bucket: "b"}, map[string]string{
		agentprotocol.SecretBackupAccessKey: "ak", agentprotocol.SecretBackupSecretKey: "sk",
	})
	require.NoError(t, err)
	s3s, ok := st.(*S3Store)
	require.True(t, ok, "an S3 destination yields an S3Store")
	require.Equal(t, "b", s3s.bucket)

	_, err = factory(agentprotocol.BackupDestination{Kind: agentprotocol.DestS3, Bucket: "b"}, nil)
	require.EqualError(t, err, "s3 credentials missing", "S3 credentials come from the secrets")

	_, err = factory(agentprotocol.BackupDestination{Kind: "ftp"}, nil)
	require.EqualError(t, err, `unknown destination kind "ftp"`)
}

func TestNewLocalStoreRequiresBackupRoot(t *testing.T) {
	_, err := NewLocalStore("", "")
	require.EqualError(t, err, "this agent has no backup directory configured")
}

func TestManifestKeyIsSidecarOfObject(t *testing.T) {
	require.Equal(t, "p/e/db/x.dump.manifest.json", ManifestKey("p/e/db/x.dump"))
	require.Equal(t, manifestKey("a"), ManifestKey("a"))
}
