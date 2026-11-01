package configsync

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
)

// display renders a value for a plan line.
func display(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case []string:
		b, _ := json.Marshal(x)
		return string(b)
	case projectconfig.ProcessValue:
		return processDisplay(x)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// asString reads a string-ish value of a catalog field.
func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case *string:
		if x != nil {
			return *x
		}
	case fmt.Stringer:
		return x.String()
	}
	return ""
}

// asInt reads a numeric value of a catalog field.
func asInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int32:
		return int(x), true
	case int64:
		return int(x), true
	case float64:
		if x != float64(int(x)) {
			return 0, false
		}
		return int(x), true
	case *int:
		if x != nil {
			return *x, true
		}
	}
	return 0, false
}

func asBool(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case *bool:
		if x != nil {
			return *x, true
		}
	}
	return false, false
}

func asFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case *float64:
		if x != nil {
			return *x, true
		}
	}
	return 0, false
}

func asStrings(v any) ([]string, bool) {
	switch x := v.(type) {
	case []string:
		return x, true
	case nil:
		return nil, true
	}
	return nil, false
}

// volumeOps handles volumes.<name>.
type volumeOps struct{}

func (volumeOps) live(pc *planCtx, ls *liveService, id, _ string) (fieldView, bool) {
	name := strings.TrimPrefix(id, "volumes.")
	for _, v := range ls.Volumes {
		if v.Name == name {
			return view(projectconfig.Volume{Name: v.Name, MountPath: v.MountPath}, v.MountPath), true
		}
	}
	return fieldView{}, false
}

func (volumeOps) desired(_ *planCtx, _ *liveService, fv projectconfig.FieldValue) (fieldView, error) {
	v, ok := fv.Value.(projectconfig.Volume)
	if !ok {
		return fieldView{}, refErr("wrong_type", "volume %s has an unexpected value", fv.ID)
	}
	return view(projectconfig.Volume{Name: v.Name, MountPath: v.MountPath}, v.MountPath), nil
}

// networkOps handles networks.<name>: attachment to a shared network.
type networkOps struct{}

func (networkOps) live(_ *planCtx, ls *liveService, id, _ string) (fieldView, bool) {
	name := strings.ToLower(strings.TrimPrefix(id, "networks."))
	for _, n := range ls.Networks {
		if strings.ToLower(n.NetworkName) == name {
			return view(map[string]any{"attached": name}, "attached"), true
		}
	}
	return fieldView{}, false
}

func (networkOps) desired(pc *planCtx, _ *liveService, fv projectconfig.FieldValue) (fieldView, error) {
	name := strings.ToLower(strings.TrimPrefix(fv.ID, "networks."))
	n, res := pc.refs().network(name)
	switch res {
	case refNone:
		return fieldView{}, refErr("unknown_network", "no shared network named %q", name)
	case refAmbiguous:
		return fieldView{}, refErr("ambiguous_reference", "several shared networks are named %q", name)
	}
	return view(map[string]any{"attached": strings.ToLower(n.Name)}, "attached"), nil
}

// linkOps handles links.<slug>: a deploy-order dependency on another service.
type linkOps struct{}

func (linkOps) live(_ *planCtx, ls *liveService, id, _ string) (fieldView, bool) {
	slug := strings.TrimPrefix(id, "links.")
	for _, l := range ls.Links {
		if l == slug {
			return view(map[string]any{"dependsOn": slug}, "depends on "+slug), true
		}
	}
	return fieldView{}, false
}

func (linkOps) desired(pc *planCtx, _ *liveService, fv projectconfig.FieldValue) (fieldView, error) {
	slug := strings.TrimPrefix(fv.ID, "links.")
	if _, inFile := pc.res.Services[slug]; !inFile {
		if _, live := pc.live.Services[slug]; !live {
			return fieldView{}, refErr("unknown_service_reference", "depends_on names %q, which is not a service of this environment", slug)
		}
	}
	return view(map[string]any{"dependsOn": slug}, "depends on "+slug), nil
}

// metaOps handles name, server and generate_domain.
type metaOps struct{}

func (metaOps) live(pc *planCtx, ls *liveService, id, _ string) (fieldView, bool) {
	switch id {
	case "name":
		return view(ls.Row.Name, ls.Row.Name), true
	case "server":
		if ls.Row.ServerID == nil {
			return view("", ""), true
		}
		name := ls.Row.ServerID.String()
		if s, ok := pc.refs().serverByID(*ls.Row.ServerID); ok {
			name = s.Name
		}
		return view(ls.Row.ServerID.String(), name), true
	case "generate_domain":
		return view(len(ls.Domains) > 0, strconv.FormatBool(len(ls.Domains) > 0)), true
	}
	return fieldView{}, false
}

func (metaOps) desired(pc *planCtx, ls *liveService, fv projectconfig.FieldValue) (fieldView, error) {
	switch fv.ID {
	case "name":
		n := strings.TrimSpace(asString(fv.Value))
		return view(n, n), nil
	case "server":
		name := asString(fv.Value)
		srv, res := pc.refs().server(name)
		switch res {
		case refNone:
			return fieldView{}, refErr("unknown_server", "no server named %q", name)
		case refAmbiguous:
			return fieldView{}, refErr("ambiguous_reference", "several servers are named %q", name)
		}
		return view(srv.ID.String(), srv.Name), nil
	case "generate_domain":
		want, _ := asBool(fv.Value)
		// Writing false asks for nothing; true asks for a domain when the service has none.
		has := ls != nil && len(ls.Domains) > 0
		if !want {
			return view(has, strconv.FormatBool(has)), nil
		}
		return view(true, "true"), nil
	}
	return fieldView{}, refErr("invalid_value", "unknown field %s", fv.ID)
}
