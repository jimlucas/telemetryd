package pathutil

import (
	"sort"
	"strconv"
	"strings"

	"github.com/openconfig/gnmi/proto/gnmi"
	"telemetryd/internal/model"
)

func Join(prefix, suffix *gnmi.Path) model.Path {
	out := model.Path{}
	if prefix != nil {
		out.Origin = prefix.GetOrigin()
		out.Target = prefix.GetTarget()
		out.Elements = append(out.Elements, elements(prefix)...)
	}
	if suffix != nil {
		if suffix.GetOrigin() != "" {
			out.Origin = suffix.GetOrigin()
		}
		if suffix.GetTarget() != "" {
			out.Target = suffix.GetTarget()
		}
		out.Elements = append(out.Elements, elements(suffix)...)
	}
	return out
}

func FromGNMI(path *gnmi.Path) model.Path {
	return Join(nil, path)
}

func elements(path *gnmi.Path) []model.PathElement {
	if path == nil {
		return nil
	}
	if len(path.GetElem()) > 0 {
		result := make([]model.PathElement, 0, len(path.GetElem()))
		for _, elem := range path.GetElem() {
			if elem == nil {
				continue
			}
			keys := make(map[string]string, len(elem.GetKey()))
			for key, value := range elem.GetKey() {
				keys[key] = value
			}
			result = append(result, model.PathElement{Name: elem.GetName(), Keys: keys})
		}
		return result
	}

	// Old gNMI encodings used the deprecated string element representation.
	result := make([]model.PathElement, 0, len(path.GetElement()))
	for _, elem := range path.GetElement() {
		result = append(result, model.PathElement{Name: elem})
	}
	return result
}

func Canonical(path model.Path) string {
	if len(path.Elements) == 0 {
		return "/"
	}
	var b strings.Builder
	for _, elem := range path.Elements {
		b.WriteByte('/')
		b.WriteString(elem.Name)
		if len(elem.Keys) == 0 {
			continue
		}
		keys := make([]string, 0, len(elem.Keys))
		for key := range elem.Keys {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			b.WriteByte('[')
			b.WriteString(key)
			b.WriteByte('=')
			b.WriteString(strconv.Quote(elem.Keys[key]))
			b.WriteByte(']')
		}
	}
	return b.String()
}

func Base(path model.Path) string {
	if len(path.Elements) == 0 {
		return "/"
	}
	var b strings.Builder
	for _, elem := range path.Elements {
		b.WriteByte('/')
		b.WriteString(elem.Name)
	}
	return b.String()
}

func FlattenKeys(path model.Path) map[string]string {
	result := make(map[string]string)
	seen := make(map[string]int)
	for _, elem := range path.Elements {
		for key, value := range elem.Keys {
			seen[key]++
			if seen[key] == 1 {
				result[key] = value
			} else {
				delete(result, key)
			}
			result[elem.Name+"_"+key] = value
		}
	}
	return result
}

func Under(candidate, prefix []model.PathElement) bool {
	if len(prefix) > len(candidate) {
		return false
	}
	for i := range prefix {
		if candidate[i].Name != prefix[i].Name {
			return false
		}
		for key, value := range prefix[i].Keys {
			if candidate[i].Keys[key] != value {
				return false
			}
		}
	}
	return true
}

func EqualElements(a, b []model.PathElement) bool {
	return len(a) == len(b) && Under(a, b) && Under(b, a)
}

func ContainsSequence(path model.Path, names ...string) bool {
	if len(names) == 0 || len(path.Elements) < len(names) {
		return false
	}
	for start := 0; start <= len(path.Elements)-len(names); start++ {
		match := true
		for i, name := range names {
			if normalize(path.Elements[start+i].Name) != normalize(name) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func EndsWith(path model.Path, names ...string) bool {
	if len(names) > len(path.Elements) {
		return false
	}
	start := len(path.Elements) - len(names)
	for i, name := range names {
		if normalize(path.Elements[start+i].Name) != normalize(name) {
			return false
		}
	}
	return true
}

func NormalizeName(value string) string {
	return normalize(value)
}

func normalize(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "")
	value = strings.ReplaceAll(value, "_", "")
	return value
}
