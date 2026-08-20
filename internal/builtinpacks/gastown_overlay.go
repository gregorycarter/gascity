package builtinpacks

import (
	"bytes"
	"io/fs"
)

const gastownRefineryFormulaPath = "formulas/mol-refinery-patrol.toml"

// patchGastownPack applies compatibility fixes at the boundary where the
// operator's pack fork is embedded. The public pack is an independent module,
// so this keeps a narrowly scoped integration fix from leaking into generic
// pack loading or bead-query semantics. Once the pack publishes the corrected
// query, this overlay becomes a no-op.
func patchGastownPack(base fs.FS) fs.FS {
	formula, err := fs.ReadFile(base, gastownRefineryFormulaPath)
	if err != nil {
		return base
	}

	stale := []byte("--assignee=$GC_AGENT --status=open \\\n")
	if !bytes.Contains(formula, stale) {
		return base
	}
	corrected := bytes.ReplaceAll(formula, stale, []byte("--assignee=$GC_AGENT --status=open,in_progress \\\n"))
	return overlayFS{base: base, files: map[string][]byte{
		gastownRefineryFormulaPath: corrected,
	}}
}

type overlayFS struct {
	base  fs.FS
	files map[string][]byte
}

func (f overlayFS) Open(name string) (fs.File, error) {
	baseFile, err := f.base.Open(name)
	if err != nil {
		return nil, err
	}
	data, ok := f.files[name]
	if !ok {
		return baseFile, nil
	}
	return &overlayFile{File: baseFile, reader: bytes.NewReader(data)}, nil
}

type overlayFile struct {
	fs.File
	reader *bytes.Reader
}

func (f *overlayFile) Read(p []byte) (int, error) {
	return f.reader.Read(p)
}
