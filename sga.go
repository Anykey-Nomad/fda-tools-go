package main

// SGA — Relic "Game data archive" reader (Dawn of War / Company of Heroes).
// The format is documented by the CDMS sources (CSgaFile.cpp from the
// Rainman library by Corsix); only versions 2 (DoW) and 4 (CoH-style) are
// supported. This is a read-only parser: browse directories lazily and
// extract entries on demand.

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	sgaMagic     = "_ARCHIVE"
	sgaInfoSize  = 24  // _SgaDataHeaderInfo
	sgaToCSize   = 140 // _SgaToC
	sgaDirSize   = 12  // _SgaDirInfo
	sgaFileEntV2 = 20  // _SgaFileInfo
	sgaFileEntV4 = 22  // _SgaFileInfo4
)

// sgaToC is a "table of contents" — a root namespace of the archive.
// Almost every archive has exactly one, aliased "Data".
type sgaToC struct {
	alias     string
	startDir  uint16
	endDir    uint16
	startFile uint16
	endFile   uint16
}

// sgaDir is one directory. name is the full path inside the archive
// (backslash-separated; empty for ToC roots), short is the last component.
// Children are half-open index ranges into SGAFile.dirs / SGAFile.files.
type sgaDir struct {
	name      string
	short     string
	subBegin  uint16
	subEnd    uint16
	fileBegin uint16
	fileEnd   uint16
}

// sgaFileEntry describes one file inside the archive. offset is relative to
// the archive's data region (SGAFile.dataOffset) and already points past the
// per-file preamble (256-byte name + CRC [+ date]), exactly as Rainman reads
// it: data begins at dataOffset+offset.
type sgaFileEntry struct {
	name    string
	dir     int
	offset  uint32
	compLen uint32
	length  uint32
}

// SGAEntry is one row returned by SGAFile.List.
type SGAEntry struct {
	Name     string
	IsDir    bool
	DirID    int
	FileID   int
	Size     int64 // files: unpacked size; dirs: number of subdirectories
	Files    int   // dirs: number of files
	CompSize int64 // files: packed size on disk
}

// SGAFile is an opened, read-only SGA archive. The underlying file stays
// open so entries can be extracted lazily; call Close when done.
type SGAFile struct {
	f          *os.File
	version    uint32
	dataOffset uint32 // absolute offset of the file-data region
	tocs       []sgaToC
	dirs       []sgaDir
	files      []sgaFileEntry
}

// OpenSGA opens and parses the index of an SGA archive.
func OpenSGA(path string) (*SGAFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	s := &SGAFile{f: f}
	if err := s.load(); err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// Close releases the archive file.
func (s *SGAFile) Close() error {
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	return err
}

// Version returns the SGA format version (2 or 4).
func (s *SGAFile) Version() uint32 { return s.version }

// ToCCount returns the number of tables of contents (entry points).
func (s *SGAFile) ToCCount() int { return len(s.tocs) }

// load parses the file header and the whole data header (index).
func (s *SGAFile) load() error {
	le := binary.LittleEndian
	r := io.Reader(s.f)

	// --- file header ---
	magic := make([]byte, 8)
	if _, err := io.ReadFull(r, magic); err != nil {
		return fmt.Errorf("read magic: %w", err)
	}
	if string(magic) != sgaMagic {
		return fmt.Errorf("not an SGA archive (magic %q)", string(magic))
	}
	if err := binary.Read(r, le, &s.version); err != nil {
		return fmt.Errorf("read version: %w", err)
	}
	if s.version != 2 && s.version != 4 {
		return fmt.Errorf("unsupported SGA version %d (only 2 and 4)", s.version)
	}
	// tool MD5 (16) + archive type (128) + header MD5 (16). The header MD5
	// is only a validator in Rainman; skipping it costs nothing here.
	if _, err := io.ReadFull(r, make([]byte, 16+128+16)); err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	var dataHeaderSize, dataOffset uint32
	if err := binary.Read(r, le, &dataHeaderSize); err != nil {
		return fmt.Errorf("read data header size: %w", err)
	}
	if err := binary.Read(r, le, &dataOffset); err != nil {
		return fmt.Errorf("read data offset: %w", err)
	}
	if s.version == 4 {
		var platform uint32
		if err := binary.Read(r, le, &platform); err != nil {
			return fmt.Errorf("read platform: %w", err)
		}
		if platform != 1 {
			return fmt.Errorf("unsupported SGA platform %d (only 1/little-endian)", platform)
		}
	}
	if dataHeaderSize == 0 || dataHeaderSize > 1<<28 {
		return fmt.Errorf("implausible data header size %d", dataHeaderSize)
	}
	s.dataOffset = dataOffset

	// --- data header (one block: tables + string pool) ---
	dh := make([]byte, dataHeaderSize)
	if _, err := io.ReadFull(r, dh); err != nil {
		return fmt.Errorf("read data header: %w", err)
	}
	if len(dh) < sgaInfoSize {
		return fmt.Errorf("data header too small (%d bytes)", len(dh))
	}

	tocOffset := le.Uint32(dh[0:4])
	tocCount := le.Uint16(dh[4:6])
	dirOffset := le.Uint32(dh[6:10])
	dirCount := le.Uint16(dh[10:12])
	fileOffset := le.Uint32(dh[12:16])
	fileCount := le.Uint16(dh[16:18])
	itemOffset := le.Uint32(dh[18:22])

	need := func(off uint32, n int, what string) error {
		end := int64(off) + int64(n)
		if end > int64(len(dh)) {
			return fmt.Errorf("corrupt archive: %s table out of bounds", what)
		}
		return nil
	}
	if err := need(tocOffset, int(tocCount)*sgaToCSize, "ToC"); err != nil {
		return err
	}
	if err := need(dirOffset, int(dirCount)*sgaDirSize, "dir"); err != nil {
		return err
	}
	fileEnt := sgaFileEntV2
	if s.version == 4 {
		fileEnt = sgaFileEntV4
	}
	if err := need(fileOffset, int(fileCount)*fileEnt, "file"); err != nil {
		return err
	}

	// String: read a NUL-terminated string from the data header, where the
	// string pool starts at itemOffset.
	str := func(nameOff uint32, what string) (string, error) {
		start := int64(itemOffset) + int64(nameOff)
		if start < int64(sgaInfoSize) || start >= int64(len(dh)) {
			return "", fmt.Errorf("corrupt archive: %s name offset out of bounds", what)
		}
		end := start
		for end < int64(len(dh)) && dh[end] != 0 {
			end++
		}
		return string(dh[start:end]), nil
	}

	// --- ToCs ---
	s.tocs = make([]sgaToC, 0, tocCount)
	for i := 0; i < int(tocCount); i++ {
		base := tocOffset + uint32(i)*sgaToCSize
		rawAlias := dh[base : base+64]
		alias := cStringZ(rawAlias)
		if s.version == 4 {
			// v4 archives store the alias in a form Rainman doesn't even
			// parse (it hardcodes 4-char paths to ToC 0 = "Data").
			alias = "Data"
			if i > 0 {
				alias = fmt.Sprintf("Data%d", i)
			}
		} else if alias == "" {
			alias = fmt.Sprintf("ToC%d", i)
		}
		s.tocs = append(s.tocs, sgaToC{
			alias:     alias,
			startDir:  le.Uint16(dh[base+128 : base+130]),
			endDir:    le.Uint16(dh[base+130 : base+132]),
			startFile: le.Uint16(dh[base+132 : base+134]),
			endFile:   le.Uint16(dh[base+134 : base+136]),
		})
		if int(s.tocs[i].startDir) >= int(dirCount) || int(s.tocs[i].endDir) > int(dirCount) {
			return fmt.Errorf("corrupt archive: ToC %d dir range out of bounds", i)
		}
	}

	// --- directories ---
	s.dirs = make([]sgaDir, dirCount)
	for i := 0; i < int(dirCount); i++ {
		base := dirOffset + uint32(i)*sgaDirSize
		name, err := str(le.Uint32(dh[base:base+4]), "dir")
		if err != nil {
			return err
		}
		short := name
		if j := strings.LastIndexByte(short, '\\'); j >= 0 {
			short = short[j+1:]
		}
		s.dirs[i] = sgaDir{
			name:      name,
			short:     short,
			subBegin:  le.Uint16(dh[base+4 : base+6]),
			subEnd:    le.Uint16(dh[base+6 : base+8]),
			fileBegin: le.Uint16(dh[base+8 : base+10]),
			fileEnd:   le.Uint16(dh[base+10 : base+12]),
		}
		d := &s.dirs[i]
		if int(d.subEnd) > int(dirCount) || int(d.fileEnd) > int(fileCount) {
			return fmt.Errorf("corrupt archive: dir %d ranges out of bounds", i)
		}
	}

	// --- files ---
	s.files = make([]sgaFileEntry, fileCount)
	for i := 0; i < int(fileCount); i++ {
		base := fileOffset + uint32(i)*uint32(fileEnt)
		name, err := str(le.Uint32(dh[base:base+4]), "file")
		if err != nil {
			return err
		}
		e := sgaFileEntry{name: name}
		if s.version == 2 {
			// nameOff, flags, offset, compLen, len
			e.offset = le.Uint32(dh[base+8 : base+12])
			e.compLen = le.Uint32(dh[base+12 : base+16])
			e.length = le.Uint32(dh[base+16 : base+20])
		} else {
			// nameOff, offset, compLen, len, modTime, flags(u16)
			e.offset = le.Uint32(dh[base+4 : base+8])
			e.compLen = le.Uint32(dh[base+8 : base+12])
			e.length = le.Uint32(dh[base+12 : base+16])
		}
		s.files[i] = e
	}

	// Attach files to their directories (files ranges already define it, but
	// keep dir id handy for navigation/debug).
	for di := range s.dirs {
		for fi := int(s.dirs[di].fileBegin); fi < int(s.dirs[di].fileEnd); fi++ {
			s.files[fi].dir = di
		}
	}

	return nil
}

// RootDir returns the root directory id of the given ToC.
func (s *SGAFile) RootDir(toc int) (int, error) {
	if toc < 0 || toc >= len(s.tocs) {
		return 0, fmt.Errorf("ToC %d out of range", toc)
	}
	return int(s.tocs[toc].startDir), nil
}

// List returns the contents of one directory: subdirectories first, then
// files, in the archive's stored (alphabetical) order.
func (s *SGAFile) List(dirID int) ([]SGAEntry, error) {
	if dirID < 0 || dirID >= len(s.dirs) {
		return nil, fmt.Errorf("dir id %d out of range", dirID)
	}
	d := s.dirs[dirID]
	out := make([]SGAEntry, 0, int(d.subEnd-d.subBegin)+int(d.fileEnd-d.fileBegin))
	for i := int(d.subBegin); i < int(d.subEnd); i++ {
		if i >= len(s.dirs) {
			return nil, fmt.Errorf("corrupt archive: subdir %d out of range", i)
		}
		out = append(out, SGAEntry{
			Name:  s.dirs[i].short,
			IsDir: true,
			DirID: i,
			Size:  int64(s.dirs[i].subEnd - s.dirs[i].subBegin),
			Files: int(s.dirs[i].fileEnd - s.dirs[i].fileBegin),
		})
	}
	for i := int(d.fileBegin); i < int(d.fileEnd); i++ {
		if i >= len(s.files) {
			return nil, fmt.Errorf("corrupt archive: file %d out of range", i)
		}
		out = append(out, SGAEntry{
			Name:     s.files[i].name,
			FileID:   i,
			Size:     int64(s.files[i].length),
			CompSize: int64(s.files[i].compLen),
		})
	}
	return out, nil
}

// ReadFile extracts one archive entry into memory, decompressing it with
// zlib when the entry was stored compressed (compLen != length).
func (s *SGAFile) ReadFile(id int) ([]byte, error) {
	if id < 0 || id >= len(s.files) {
		return nil, fmt.Errorf("file id %d out of range", id)
	}
	fe := s.files[id]
	if fe.length > 1<<30 {
		return nil, fmt.Errorf("%s: implausible size %d", fe.name, fe.length)
	}
	raw := make([]byte, fe.compLen)
	if _, err := s.f.ReadAt(raw, int64(s.dataOffset)+int64(fe.offset)); err != nil {
		return nil, fmt.Errorf("read %s: %w", fe.name, err)
	}
	if fe.compLen == fe.length {
		return raw, nil
	}
	out := make([]byte, fe.length)
	zr, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("decompress %s: %w", fe.name, err)
	}
	defer zr.Close()
	if _, err := io.ReadFull(zr, out); err != nil {
		return nil, fmt.Errorf("decompress %s: %w", fe.name, err)
	}
	return out, nil
}

// cStringZ copies bytes up to the first NUL (or the end).
func cStringZ(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}
