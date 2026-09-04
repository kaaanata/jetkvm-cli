// Command buildoverlay applies narrowly scoped fail-closed fixes to the pinned
// hi264 sources without changing the module cache or maintaining a second fork.
package main

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) != 3 {
		panic("usage: buildoverlay MODULE_DIR OUTPUT_DIR")
	}
	patches := []struct{ path, old, replacement string }{
		{"internal/cabac/cabac.go", "\t\t} else {\n\t\t\treturn 0\n\t\t}", "\t\t} else {\n\t\t\tpanic(\"truncated CABAC input\")\n\t\t}"},
		{"internal/slice/slicedata.go", "\t\t\tif endOfSlice == 1 {\n\t\t\t\tbreak\n\t\t\t}", "\t\t\tif endOfSlice == 1 {\n\t\t\t\treturn nil, fmt.Errorf(\"incomplete IDR: end of slice at macroblock %d of %d\", mbIdx+1, totalMBs)\n\t\t\t}"},
	}
	overlay := struct{ Replace map[string]string }{Replace: make(map[string]string)}
	for _, p := range patches {
		source := filepath.Join(os.Args[1], p.path)
		data, err := os.ReadFile(source)
		if err != nil {
			panic(err)
		}
		if strings.Count(string(data), p.old) != 1 {
			panic(fmt.Sprintf("upstream patch mismatch: %s", p.path))
		}
		dest := filepath.Join(os.Args[2], filepath.Base(p.path)+".txt")
		if err := os.WriteFile(dest, []byte(strings.Replace(string(data), p.old, p.replacement, 1)), 0600); err != nil {
			panic(err)
		}
		overlay.Replace[source] = dest
	}
	data, err := json.Marshal(overlay)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(os.Args[2], "overlay.json"), data, 0600); err != nil {
		panic(err)
	}
}
