package main

// The tools mcpd offers: names, descriptions written for a model, and JSON
// Schemas (2020-12) for arguments and results. tools.json holds them,
// byte for byte as the Rust sibling's mcpd lists them. validate checks
// arguments against the subset of JSON Schema these definitions use.

import (
	_ "embed"
	"errors"
	"fmt"
	"strings"

	"github.com/ericvh/purgatorio-go/sdk/mcp"
)

const (
	defaultTimeoutMS = 30_000
	maxTimeoutMS     = 600_000
	// defaultOutput is the cap on each of stdout and stderr, in bytes.
	defaultOutput = 64 * 1024
	// defaultRead is the default read_file length.
	defaultRead = 64 * 1024
	// maxEntries is the most entries list_dir returns.
	maxEntries = 5000
	// jobBuffer is what is kept of each of a job's stdout and stderr: the
	// last this many bytes.
	jobBuffer = 1024 * 1024
	// maxJobs is the jobs that may run at once, and exited jobs kept for
	// job_read.
	maxJobs = 32
	// mountTimeoutMS is the default timeout of mount.
	mountTimeoutMS = 30_000
	// maxResource is the largest file resources/read returns.
	maxResource = 1024 * 1024
	// maxResources is the most files resources/list lists.
	maxResources = 1000
)

//go:embed tools.json
var toolsJSON string

//go:embed primer.txt
var primer string

var toolList []mcp.Value

// tools is every tool, in the order tools/list gives them.
func tools() []mcp.Value {
	if toolList == nil {
		v, err := mcp.Parse(strings.TrimSpace(toolsJSON))
		if err != nil {
			panic("tools.json: " + err.Error())
		}
		toolList = v.([]mcp.Value)
	}
	return toolList
}

func findTool(name string) mcp.Value {
	for _, t := range tools() {
		if n, _ := mcp.Str(t, "name"); n == name {
			return t
		}
	}
	return nil
}

// check checks what a schema here cannot say: job_start takes exactly one
// of command and argv, and argv names a program.
func check(name string, args mcp.Value) error {
	if name != "job_start" {
		return nil
	}
	c, hasC := mcp.Get(args, "command")
	a, hasA := mcp.Get(args, "argv")
	_ = c
	switch {
	case hasC && hasA:
		return errors.New("give arguments.command or arguments.argv, not both")
	case !hasC && !hasA:
		return errors.New("arguments.command or arguments.argv is required")
	case !hasC:
		if l, ok := a.([]mcp.Value); ok && len(l) == 0 {
			return errors.New("arguments.argv must not be empty")
		}
	}
	return nil
}

// validate checks v against schema. It understands type (object, string,
// integer, boolean, array), properties, required,
// additionalProperties: false, enum, minimum, maximum and items, which is
// all the tool schemas use. at names the value in the error text.
func validate(v, schema mcp.Value, at string) error {
	want, _ := mcp.Str(schema, "type")
	ok := true
	switch want {
	case "object":
		_, ok = v.(mcp.Obj)
	case "string":
		_, ok = v.(string)
	case "integer":
		_, ok = mcp.AsInt(v)
	case "boolean":
		_, ok = v.(bool)
	case "array":
		_, ok = v.([]mcp.Value)
	}
	if !ok {
		return fmt.Errorf("%s must be of type %s, not %s", at, want, mcp.TypeName(v))
	}
	if e, _ := mcp.Get(schema, "enum"); e != nil {
		if allowed, isArr := e.([]mcp.Value); isArr {
			found := false
			var list []string
			for _, x := range allowed {
				if mcp.Equal(x, v) {
					found = true
				}
				list = append(list, mcp.Encode(x))
			}
			if !found {
				return fmt.Errorf("%s must be one of %s", at, strings.Join(list, ", "))
			}
		}
	}
	if n, isInt := mcp.AsInt(v); isInt {
		if lo, ok := mcp.Int(schema, "minimum"); ok && n < lo {
			return fmt.Errorf("%s must be at least %d", at, lo)
		}
		if hi, ok := mcp.Int(schema, "maximum"); ok && n > hi {
			return fmt.Errorf("%s must be at most %d", at, hi)
		}
	}
	if items, isArr := v.([]mcp.Value); isArr {
		if item, ok := mcp.Get(schema, "items"); ok {
			for i, x := range items {
				if err := validate(x, item, fmt.Sprintf("%s[%d]", at, i)); err != nil {
					return err
				}
			}
		}
	}
	if members, isObj := v.(mcp.Obj); isObj {
		props, _ := mcp.Get(schema, "properties")
		if r, _ := mcp.Get(schema, "required"); r != nil {
			if req, ok := r.([]mcp.Value); ok {
				for _, x := range req {
					if k, ok := x.(string); ok && !mcp.Has(v, k) {
						return fmt.Errorf("%s.%s is required", at, k)
					}
				}
			}
		}
		ap, _ := mcp.Get(schema, "additionalProperties")
		closed := ap == false
		for _, m := range members {
			if s, ok := mcp.Get(props, m.K); ok {
				if err := validate(m.V, s, at+"."+m.K); err != nil {
					return err
				}
			} else if closed {
				return fmt.Errorf("%s.%s is not a known property", at, m.K)
			}
		}
	}
	return nil
}
