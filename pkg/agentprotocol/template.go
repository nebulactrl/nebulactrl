package agentprotocol

// FileSpec is a file the agent writes into a container (tar copy to a created,
// not yet started container) so it is in place at first start.
type FileSpec struct {
	Path    string `json:"path"`
	Mode    string `json:"mode,omitempty"` // octal, default 0644
	Content string `json:"content"`
}

// LabelOneShot marks containers of one-shot services; the agent reports a
// clean exit (code 0) of such a container as healthy.
const LabelOneShot = "nebulactrl.oneshot"
