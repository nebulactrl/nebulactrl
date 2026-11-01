package projectconfig

import _ "embed"

// SchemaID is the $id of the JSON Schema of nebula.toml. The host is a
// placeholder: the server that serves the file answers on its own origin.
const SchemaID = "https://origin.nebulactrl.invalid/schema/nebula.toml.json"

//go:embed schema/nebula.toml.json
var schemaJSON []byte

// Schema returns the JSON Schema of nebula.toml (draft 2020-12), the bytes of
// schema/nebula.toml.json. The slice is shared: callers must not modify it.
func Schema() []byte { return schemaJSON }
