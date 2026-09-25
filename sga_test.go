package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildTestSGA constructs a minimal but structurally complete SGA archive:
//
//	ToC "Data"
//	├── (root)   a.bin  — stored raw
//	�── music/   b.bin  — stored zlib-compressed
//
// It mirrors the exact layout written by Relic's tools (and read by
// Rainman's CSgaFile): header, data header (info/ToC/dirs/files/strings),
// then the data region.
func buildTestSGA(t *testing.T, version uint32) []byte {
	t.Helper()
	le := binary.LittleEndian

	fileEnt := uint32(sgaFileEntV2)
	if version == 4 {
		fileEnt = sgaFileEntV4
	}
	headerSize := 180
	if version == 4 {
		headerSize = 184
	}

	// --- string pool ---
	items := []byte{0} // offset 0: "" (root dir name)
	strOff := map[string]uint32{"": 0}
	addStr := func(s string) {
		if _, ok := strOff[s]; ok {
			return
		}
		strOff[s] = uint32(len(items))
		items = append(items, s...)
		items = append(items, 0)
	}
	addStr("music")
	addStr("a.bin")
	addStr("b.bin")

	// --- table layout inside the data header ---
	tocOff := uint32(sgaInfoSize)
	dirOff := tocOff + sgaToCSize
	fileOff := dirOff + 2*sgaDirSize
	itemOff := fileOff + 2*fileEnt
	dhSize := itemOff + uint32(len(items))

	dh := make([]byte, dhSize)
	// _SgaDataHeaderInfo
	le.PutUint32(dh[0:4], tocOff)
	le.PutUint16(dh[4:6], 1) // toc count
	le.PutUint32(dh[6:10], dirOff)
	le.PutUint16(dh[10:12], 2) // dir count (root + music)
	le.PutUint32(dh[12:16], fileOff)
	le.PutUint16(dh[16:18], 2) // file count
	le.PutUint32(dh[18:22], itemOff)
	le.PutUint16(dh[22:24], uint16(len(items)))

	// _SgaToC: alias[64], baseDirName[64], startDir, endDir, startFile, endFile, folderOffset
	copy(dh[tocOff:], "Data")
	le.PutUint16(dh[tocOff+128:], 0) // startDir
	le.PutUint16(dh[tocOff+130:], 2) // endDir
	le.PutUint16(dh[tocOff+132:], 0) // startFile
	le.PutUint16(dh[tocOff+134:], 2) // endFile

	// dir 0 = root: subdirs [1,2), files [0,1)
	copy(dh[dirOff:], "") // name offset 0
	le.PutUint32(dh[dirOff:], 0)
	le.PutUint16(dh[dirOff+4:], 1)
	le.PutUint16(dh[dirOff+6:], 2)
	le.PutUint16(dh[dirOff+8:], 0)
	le.PutUint16(dh[dirOff+10:], 1)

	// dir 1 = "music": no subdirs, files [1,2)
	b1 := dirOff + sgaDirSize
	le.PutUint32(dh[b1:], strOff["music"])
	le.PutUint16(dh[b1+4:], 1)
	le.PutUint16(dh[b1+6:], 1)
	le.PutUint16(dh[b1+8:], 1)
	le.PutUint16(dh[b1+10:], 2)

	// --- payloads ---
	rawPayload := []byte("raw fda payload bytes")
	zPayload := []byte(strings.Repeat("compressed payload — длинный русский текст для проверки. ", 4))

	var zBuf bytes.Buffer
	zw := zlib.NewWriter(&zBuf)
	if _, err := zw.Write(zPayload); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zCompressed := zBuf.Bytes()
	if len(zCompressed) >= len(zPayload) {
		t.Fatal("test payload too short to actually compress")
	}

	putFile := func(base uint32, nameOff uint32, offset, compLen, length uint32) {
		le.PutUint32(dh[base:], nameOff)
		if version == 2 {
			le.PutUint32(dh[base+4:], 0)        // flags: 0 = uncompressed entry meta
			le.PutUint32(dh[base+8:], offset)   // data offset
			le.PutUint32(dh[base+12:], compLen) // compressed length
			le.PutUint32(dh[base+16:], length)  // length
		} else {
			le.PutUint32(dh[base+4:], offset)
			le.PutUint32(dh[base+8:], compLen)
			le.PutUint32(dh[base+12:], length)
			le.PutUint32(dh[base+16:], 0) // modification time
			le.PutUint16(dh[base+20:], 0) // flags
		}
	}
	putFile(fileOff, strOff["a.bin"], 0, uint32(len(rawPayload)), uint32(len(rawPayload)))
	putFile(fileOff+fileEnt, strOff["b.bin"], uint32(len(rawPayload)), uint32(len(zCompressed)), uint32(len(zPayload)))

	copy(dh[itemOff:], items)

	// --- file header ---
	hdr := make([]byte, headerSize)
	copy(hdr, sgaMagic)
	le.PutUint32(hdr[8:], version)
	// [12:28) tool MD5, [28:156) archive type, [156:172) header MD5 — zeros
	le.PutUint32(hdr[172:], uint32(len(dh)))            // data header size
	le.PutUint32(hdr[176:], uint32(headerSize+len(dh))) // data offset
	if version == 4 {
		le.PutUint32(hdr[180:], 1) // platform: 1 = little-endian
	}

	var out bytes.Buffer
	out.Write(hdr)
	out.Write(dh)
	out.Write(rawPayload)
	out.Write(zCompressed)
	return out.Bytes()
}

func checkArchiveContents(t *testing.T, path string) {
	t.Helper()
	s, err := OpenSGA(path)
	if err != nil {
		t.Fatalf("OpenSGA: %v", err)
	}
	defer s.Close()

	if s.ToCCount() != 1 {
		t.Fatalf("ToCCount = %d, want 1", s.ToCCount())
	}
	root, err := s.RootDir(0)
	if err != nil {
		t.Fatal(err)
	}
	top, err := s.List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 {
		t.Fatalf("root entries = %d, want 2 (%+v)", len(top), top)
	}
	if !top[0].IsDir || top[0].Name != "music" {
		t.Errorf("top[0] = %+v, want dir \"music\"", top[0])
	}
	if top[1].IsDir || top[1].Name != "a.bin" {
		t.Errorf("top[1] = %+v, want file \"a.bin\"", top[1])
	}

	sub, err := s.List(top[0].DirID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sub) != 1 || sub[0].Name != "b.bin" {
		t.Fatalf("music entries = %+v, want [b.bin]", sub)
	}

	// raw entry
	data, err := s.ReadFile(top[1].FileID)
	if err != nil {
		t.Fatalf("ReadFile(a.bin): %v", err)
	}
	if string(data) != "raw fda payload bytes" {
		t.Errorf("a.bin content = %q", data)
	}

	// zlib entry
	data, err = s.ReadFile(sub[0].FileID)
	if err != nil {
		t.Fatalf("ReadFile(b.bin): %v", err)
	}
	if !bytes.Contains(data, []byte("длинный русский текст")) {
		t.Errorf("b.bin content = %q", data)
	}
}

func TestSGAv2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test_v2.sga")
	if err := os.WriteFile(path, buildTestSGA(t, 2), 0o644); err != nil {
		t.Fatal(err)
	}
	checkArchiveContents(t, path)
}

func TestSGAv4(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test_v4.sga")
	if err := os.WriteFile(path, buildTestSGA(t, 4), 0o644); err != nil {
		t.Fatal(err)
	}
	checkArchiveContents(t, path)
}

func TestSGARejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "junk.sga")
	if err := os.WriteFile(path, []byte("this is definitely not an archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSGA(path); err == nil {
		t.Fatal("OpenSGA accepted garbage")
	}
}

// TestSGARealMusicArchive smoke-tests the parser against the real Dawn of War
// Definitive Edition archives when the game is installed; skips otherwise.
func TestSGARealMusicArchive(t *testing.T) {
	base := `C:\Program Files\Steam\steamapps\common\Dawn of War Definitive Edition\DXP2`
	for _, name := range []string{"DXP2Data-Music.sga", "DXP2Data-Sound-Full.sga"} {
		path := filepath.Join(base, name)
		if _, err := os.Stat(path); err != nil {
			t.Logf("%s not found, skipping", path)
			continue
		}
		s, err := OpenSGA(path)
		if err != nil {
			t.Fatalf("OpenSGA(%s): %v", name, err)
		}
		t.Logf("%s: version=%d, ToCs=%d, dirs=%d, files=%d",
			name, s.Version(), s.ToCCount(), len(s.dirs), len(s.files))

		fdaCount := 0
		var firstFDA *SGAEntry
		var walk func(dirID int, depth int)
		walk = func(dirID int, depth int) {
			entries, err := s.List(dirID)
			if err != nil {
				t.Errorf("List(%d): %v", dirID, err)
				return
			}
			for _, e := range entries {
				if e.IsDir {
					walk(e.DirID, depth+1)
				} else if filepath.Ext(e.Name) == ".fda" {
					fdaCount++
					if firstFDA == nil {
						fe := e
						firstFDA = &fe
					}
				}
			}
		}
		root, _ := s.RootDir(0)
		walk(root, 0)
		t.Logf("%s: %d .fda files", name, fdaCount)
		if fdaCount == 0 {
			t.Errorf("%s: no .fda entries found", name)
		}

		// Extract the first FDA and parse it — validates the whole
		// extraction chain (offsets + zlib) against real data.
		if firstFDA != nil {
			data, err := s.ReadFile(firstFDA.FileID)
			if err != nil {
				t.Errorf("ReadFile(%s): %v", firstFDA.Name, err)
			} else {
				fda, err := ParseFDAReader(bytes.NewReader(data))
				if err != nil {
					t.Errorf("ParseFDAReader(%s): %v", firstFDA.Name, err)
				} else {
					t.Logf("extracted %s: %d Hz, %d ch, %d bytes PCM",
						firstFDA.Name, fda.Info.SampleRate, fda.Info.Channels, len(fda.RawData))
				}
			}
		}
		s.Close()
	}
}
