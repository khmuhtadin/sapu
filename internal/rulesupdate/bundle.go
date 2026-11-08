package rulesupdate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"strconv"
	"strings"
	"testing/fstest"
	"time"

	"github.com/khmuhtadin/sapu/internal/protect"
	"github.com/khmuhtadin/sapu/internal/purge"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// Bundle is the JSON document of a rule update. Each map holds every YAML
// file of one rules folder, file name -> text.
type Bundle struct {
	Version  string            `json:"version"`  // dotted numbers, e.g. "2026.11.20"
	Created  time.Time         `json:"created"`  // when sapu-rules bundle made it
	MinSapu  string            `json:"min_sapu"` // oldest Sapu that may use it, e.g. "1.2.0"
	Cleaners map[string]string `json:"cleaners"` // rules/cleaners/*.yml
	Protect  map[string]string `json:"protect"`  // rules/protect/*.yml
	Purge    map[string]string `json:"purge"`    // rules/purge/*.yml
}

// Parts are the rule folders a bundle holds, in the order of Bundle.
var Parts = []string{"cleaners", "protect", "purge"}

// versionPattern is the form of a bundle version and of min_sapu.
var versionPattern = regexp.MustCompile(`^[0-9]{1,9}(\.[0-9]{1,9}){0,3}$`)

// fileName is the form of a rule file name inside a bundle: no folders.
var fileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.yml$`)

// Check reads a bundle and checks every rule file in it with the loaders
// Sapu uses: rules.Load for cleaners, protect.ValidateFS and
// purge.ValidateTypes. Unknown fields are errors.
func Check(data []byte) (Bundle, error) {
	var b Bundle
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return Bundle{}, fmt.Errorf("not a rule bundle: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Bundle{}, errors.New("not a rule bundle: more data after the JSON document")
	}
	switch {
	case !versionPattern.MatchString(b.Version):
		return Bundle{}, fmt.Errorf("version %q must be dotted numbers like 2026.11.20", b.Version)
	case b.Created.IsZero():
		return Bundle{}, errors.New("created is missing")
	case !versionPattern.MatchString(b.MinSapu):
		return Bundle{}, fmt.Errorf("min_sapu %q must be a Sapu version like 1.2.0", b.MinSapu)
	}
	for i, files := range []map[string]string{b.Cleaners, b.Protect, b.Purge} {
		if len(files) == 0 {
			return Bundle{}, fmt.Errorf("%s holds no files; a bundle carries every rule file", Parts[i])
		}
		for name := range files {
			if !fileName.MatchString(name) {
				return Bundle{}, fmt.Errorf("%s: %q is not a rule file name like apps.yml", Parts[i], name)
			}
		}
	}
	cleaners, protectFiles, purgeFiles := b.FS()
	if _, err := rules.Load(cleaners); err != nil {
		return Bundle{}, fmt.Errorf("cleaners: %w", err)
	}
	if err := protect.ValidateFS(protectFiles); err != nil {
		return Bundle{}, fmt.Errorf("protect: %w", err)
	}
	if err := purge.ValidateTypes(purgeFiles); err != nil {
		return Bundle{}, fmt.Errorf("purge: %w", err)
	}
	return b, nil
}

// FS returns the three rule folders of the bundle as file systems.
func (b Bundle) FS() (cleaners, protectFiles, purgeFiles fs.FS) {
	return mapFS(b.Cleaners), mapFS(b.Protect), mapFS(b.Purge)
}

// mapFS holds files in memory. fstest.MapFS is the standard library's plain
// map-backed fs.FS; nothing of package testing comes with it.
func mapFS(files map[string]string) fs.FS {
	m := fstest.MapFS{}
	for name, text := range files {
		m[name] = &fstest.MapFile{Data: []byte(text), Mode: 0o444}
	}
	return m
}

// Build makes a bundle from a folder holding cleaners/, protect/ and purge/
// (like the rules folder of the source tree): the *.yml files directly
// inside each. It checks the result as Install would.
func Build(dir fs.FS, version, minSapu string, created time.Time) ([]byte, error) {
	b := Bundle{Version: version, Created: created.UTC().Truncate(time.Second), MinSapu: minSapu}
	for _, part := range Parts {
		names, err := fs.Glob(dir, part+"/*.yml")
		if err != nil {
			return nil, err
		}
		files := map[string]string{}
		for _, name := range names {
			data, err := fs.ReadFile(dir, name)
			if err != nil {
				return nil, err
			}
			files[path.Base(name)] = string(data)
		}
		switch part {
		case "cleaners":
			b.Cleaners = files
		case "protect":
			b.Protect = files
		case "purge":
			b.Purge = files
		}
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if _, err := Check(data); err != nil {
		return nil, err
	}
	return data, nil
}

// compareVersions compares two dotted versions part by part as numbers;
// missing parts count as 0. Both must match versionPattern.
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(pa), len(pb)) {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i]) // checked by versionPattern
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x - y
		}
	}
	return 0
}

// sapuNumber turns a Sapu version like "v1.2.3", "1.2.3-rc.1" or
// "1.2.3-4-gabcdef" into "1.2.3". ok is false for "dev" and other builds
// without a version number.
func sapuNumber(v string) (string, bool) {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	return v, versionPattern.MatchString(v)
}

// sapuAtLeast reports whether Sapu version sapu is min or newer. A build
// without a version number ("dev", built from source) counts as newest.
func sapuAtLeast(sapu, min string) bool {
	n, ok := sapuNumber(sapu)
	return !ok || compareVersions(n, min) >= 0
}
