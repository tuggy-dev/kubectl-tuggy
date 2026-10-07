package v1alpha1

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"
)

// maxSpecSize bounds how much of a spec file is read.
const maxSpecSize = 1 << 20

// Load reads a Cluster from YAML or JSON. Unknown or duplicated fields are
// errors, so typos are caught instead of silently ignored. Load does not
// apply defaults or validate; call SetDefaults and Validate afterwards.
func Load(r io.Reader) (*Cluster, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxSpecSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSpecSize {
		return nil, fmt.Errorf("cluster spec is larger than %d bytes", maxSpecSize)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errors.New("cluster spec is empty")
	}
	if hasMultipleDocuments(data) {
		return nil, errors.New("cluster spec must contain a single document; found a '---' separator")
	}

	// Check apiVersion and kind first so a document of the wrong type gets a
	// clear message rather than a list of unknown fields.
	var tm TypeMeta
	if err := yaml.Unmarshal(data, &tm); err != nil {
		return nil, fmt.Errorf("cluster spec is not valid YAML: %w", err)
	}
	if tm.Kind != KindCluster {
		return nil, fmt.Errorf("kind must be %q, got %q", KindCluster, tm.Kind)
	}
	if tm.APIVersion != APIVersion {
		return nil, fmt.Errorf("apiVersion must be %q, got %q", APIVersion, tm.APIVersion)
	}

	var c Cluster
	if err := yaml.UnmarshalStrict(data, &c); err != nil {
		return nil, fmt.Errorf("invalid cluster spec: %s", describeDecodeError(data, err))
	}
	return &c, nil
}

// LoadFile reads a Cluster from a file. See Load.
func LoadFile(path string) (*Cluster, error) {
	f, err := os.Open(path) // #nosec G304 -- the user chooses which spec file to read
	if err != nil {
		return nil, err
	}
	defer f.Close()

	c, err := Load(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// DecodePlatformConfig decodes spec.platformConfig into out, rejecting
// unknown fields. Platforms call it to read their own settings. An empty
// platformConfig leaves out unchanged.
func (s *ClusterSpec) DecodePlatformConfig(out any) error {
	raw := bytes.TrimSpace(s.PlatformConfig)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("spec.platformConfig: %w", err)
	}
	return nil
}

func hasMultipleDocuments(data []byte) bool {
	seenContent := false
	for line := range strings.Lines(string(data)) {
		trimmed := strings.TrimRight(line, " \t\r\n")
		if trimmed == "---" || strings.HasPrefix(trimmed, "--- ") {
			if seenContent {
				return true
			}
			continue
		}
		t := strings.TrimSpace(trimmed)
		if t != "" && !strings.HasPrefix(t, "#") {
			seenContent = true
		}
	}
	return false
}

var (
	unknownFieldPattern = regexp.MustCompile(`unknown field "([^"]+)"`)
	decodePrefixes      = []string{"error unmarshaling JSON: ", "while decoding JSON: ", "json: "}
)

// describeDecodeError turns a decoding error into a short message. For an
// unknown field it reports the field's full path, such as
// "spec.nodePools[0].cnt", because the JSON decoder only reports the name.
func describeDecodeError(data []byte, err error) string {
	msg := err.Error()
	for _, p := range decodePrefixes {
		msg = strings.TrimPrefix(msg, p)
	}

	m := unknownFieldPattern.FindStringSubmatch(msg)
	if m == nil {
		return msg
	}
	var doc any
	if yaml.Unmarshal(data, &doc) != nil {
		return msg
	}
	if paths := findKey(doc, m[1], ""); len(paths) > 0 {
		return fmt.Sprintf("unknown field %q", paths[0])
	}
	return msg
}

// findKey returns the paths of every map key equal to name, skipping
// spec.platformConfig whose fields are checked by the platform.
func findKey(node any, name, path string) []string {
	var found []string
	switch n := node.(type) {
	case map[string]any:
		keys := make([]string, 0, len(n))
		for k := range n {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child := k
			if path != "" {
				child = path + "." + k
			}
			if child == "spec.platformConfig" {
				continue
			}
			if k == name {
				found = append(found, child)
			}
			found = append(found, findKey(n[k], name, child)...)
		}
	case []any:
		for i, v := range n {
			found = append(found, findKey(v, name, path+"["+strconv.Itoa(i)+"]")...)
		}
	}
	return found
}
