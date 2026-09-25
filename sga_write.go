package main

import (
	"bytes"
	"compress/zlib"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// filePayload holds uncompressed and (maybe) compressed data for one archive entry.
type filePayload struct {
	uncomp  []byte
	comp    []byte
	useComp bool
	flag    uint32
}

// RebuildSGA repacks src SGA into dst, recompressing every file. If replacements
// is non-nil it maps lower-cased full paths (e.g. "sound\music\foo.fda") to
// new uncompressed bytes that will replace the original file content.
// Progress is called after each file is compressed; if nil a default
// console progress bar with a warning is printed.
//
// (dst must not be the same file as src; the caller should copy the original
// to a temp location first — this function will refuse to overwrite src.)
func RebuildSGA(src, dst string, replacements map[string][]byte, progress func(done, total int)) error {
	if strings.EqualFold(filepath.Clean(src), filepath.Clean(dst)) {
		return fmt.Errorf("refusing to overwrite source archive in place: %s", src)
	}
	s, err := OpenSGA(src)
	if err != nil {
		return err
	}
	defer s.Close()

	total := len(s.files)
	if total == 0 {
		return fmt.Errorf("archive has no files")
	}

	// --- Warning ---
	if progress == nil {
		fmt.Println("⚠️  Перепаковка SGA займет некоторое время — идет сжатие и пересборка архива...")
		fmt.Printf("   %s → %s  (%d файлов, версия %d)\n", filepath.Base(src), filepath.Base(dst), total, s.version)
	}

	bar := func(done, tot int) {
		if progress != nil {
			progress(done, tot)
			return
		}
		w := 30
		filled := done * w / tot
		if filled > w {
			filled = w
		}
		bar := strings.Repeat("█", filled) + strings.Repeat("░", w-filled)
		fmt.Printf("\r  [%s] %d/%d", bar, done, tot)
		if done == tot {
			fmt.Println()
		}
	}

	// --- Load and (re)compress every file ---
	payloads := make([]filePayload, total)
	for i, fe := range s.files {
		full := s.fileFullPath(i)
		key := strings.ToLower(full)
		var uncomp []byte
		if replacements != nil {
			if rep, ok := replacements[key]; ok {
				// copy to avoid aliasing
				uncomp = make([]byte, len(rep))
				copy(uncomp, rep)
			}
		}
		if uncomp == nil {
			uncomp, err = s.ReadFile(i)
			if err != nil {
				return fmt.Errorf("read %s (%s): %w", fe.name, full, err)
			}
		}
		payloads[i].uncomp = uncomp

		// zlib compress (best compression like CSgaCreator)
		var cbuf bytes.Buffer
		zw, _ := zlib.NewWriterLevel(&cbuf, zlib.BestCompression)
		if _, err := zw.Write(uncomp); err != nil {
			return err
		}
		zw.Close()
		comp := cbuf.Bytes()
		if len(comp) < len(uncomp) {
			// store compressed
			cp := make([]byte, len(comp))
			copy(cp, comp)
			payloads[i].comp = cp
			payloads[i].useComp = true
			if s.version == 2 {
				if len(uncomp) < 4096 {
					payloads[i].flag = 0x20
				} else {
					payloads[i].flag = 0x10
				}
			} else {
				payloads[i].flag = 0x100
			}
		} else {
			payloads[i].flag = 0
		}
		_ = fe // keep
		bar(i+1, total)
	}

	// --- Build string pool and data header ---
	// String pool: dirs full names (in index order) + files short names (in index order)
	type strOff struct {
		off uint32
	}
	pool := []byte{}
	offMap := map[string]uint32{}
	// dir names
	dirNameOff := make([]uint32, len(s.dirs))
	for i, d := range s.dirs {
		name := d.name // full path, may be ""
		if off, ok := offMap[name]; ok {
			dirNameOff[i] = off
			continue
		}
		off := uint32(len(pool))
		offMap[name] = off
		dirNameOff[i] = off
		pool = append(pool, name...)
		pool = append(pool, 0)
	}
	fileNameOff := make([]uint32, len(s.files))
	for i, fe := range s.files {
		name := fe.name
		if off, ok := offMap[name]; ok {
			// file names are often unique, but still dedup
			fileNameOff[i] = off
			continue
		}
		off := uint32(len(pool))
		offMap[name] = off
		fileNameOff[i] = off
		pool = append(pool, name...)
		pool = append(pool, 0)
	}
	// But the above dedup would reuse same offset for same name across different dirs/files,
	// which is correct per SGA (names are stored once). However dirs and files reuse same pool,
	// but we started pool with dirs then files — dedup across them is valid.

	// Recompute itemCount = number of unique strings, not dir+file count if dedup.
	// Original CSgaCreator uses iFileCount+iDirCount, but with dedup it would be less.
	// For determinism, keep CSgaCreator's itemCount = dirCount+fileCount (even if strings deduped,
	// pool will be smaller and offsets will still be valid because duplicates point to same offset).
	// The header's itemCount is not validated strictly by our reader.
	itemCount := uint16(len(s.dirs) + len(s.files))

	fileEnt := sgaFileEntV2
	if s.version == 4 {
		fileEnt = sgaFileEntV4
	}
	tocCount := len(s.tocs)
	dirCount := len(s.dirs)
	fileCount := len(s.files)

	tocOff := uint32(sgaInfoSize)
	dirOff := tocOff + uint32(tocCount)*sgaToCSize
	fileOff := dirOff + uint32(dirCount)*uint32(sgaDirSize)
	itemOff := fileOff + uint32(fileCount)*uint32(fileEnt)
	dhSize := itemOff + uint32(len(pool))
	dh := make([]byte, dhSize)
	le := binary.LittleEndian
	le.PutUint32(dh[0:4], tocOff)
	le.PutUint16(dh[4:6], uint16(tocCount))
	le.PutUint32(dh[6:10], dirOff)
	le.PutUint16(dh[10:12], uint16(dirCount))
	le.PutUint32(dh[12:16], fileOff)
	le.PutUint16(dh[16:18], uint16(fileCount))
	le.PutUint32(dh[18:22], itemOff)
	le.PutUint16(dh[22:24], itemCount)

	// ToCs
	for i, toc := range s.tocs {
		base := tocOff + uint32(i)*sgaToCSize
		// alias 64
		alias := toc.alias
		if s.version == 4 {
			// v4 alias is hardcoded "Data", but keep what we parsed
			alias = toc.alias
		}
		copy(dh[base:base+64], alias)
		// baseDirName 64 zeros already
		le.PutUint16(dh[base+128:base+130], toc.startDir)
		le.PutUint16(dh[base+130:base+132], toc.endDir)
		le.PutUint16(dh[base+132:base+134], toc.startFile)
		le.PutUint16(dh[base+134:base+136], toc.endFile)
		le.PutUint32(dh[base+136:base+140], 0)
	}
	// Dirs
	for i, d := range s.dirs {
		base := dirOff + uint32(i)*sgaDirSize
		le.PutUint32(dh[base:base+4], dirNameOff[i])
		le.PutUint16(dh[base+4:base+6], d.subBegin)
		le.PutUint16(dh[base+6:base+8], d.subEnd)
		le.PutUint16(dh[base+8:base+10], d.fileBegin)
		le.PutUint16(dh[base+10:base+12], d.fileEnd)
	}
	// Compute file offsets (including preambles)
	preSize := uint32(0)
	if s.version == 2 {
		preSize = 264
	} else if s.version == 4 {
		preSize = 260
	}
	offs := make([]uint32, fileCount)
	var cum uint32
	for i := range payloads {
		offs[i] = cum + preSize
		clen := uint32(len(payloads[i].uncomp))
		if payloads[i].useComp {
			clen = uint32(len(payloads[i].comp))
		}
		cum += preSize + clen
	}
	// Files
	for i := range s.files {
		base := fileOff + uint32(i)*uint32(fileEnt)
		le.PutUint32(dh[base:base+4], fileNameOff[i])
		pl := payloads[i]
		clen := uint32(len(pl.uncomp))
		ulen := uint32(len(pl.uncomp))
		if pl.useComp {
			clen = uint32(len(pl.comp))
		}
		if s.version == 2 {
			le.PutUint32(dh[base+4:base+8], pl.flag)
			le.PutUint32(dh[base+8:base+12], offs[i])
			le.PutUint32(dh[base+12:base+16], clen)
			le.PutUint32(dh[base+16:base+20], ulen)
		} else {
			le.PutUint32(dh[base+4:base+8], offs[i])
			le.PutUint32(dh[base+8:base+12], clen)
			le.PutUint32(dh[base+12:base+16], ulen)
			le.PutUint32(dh[base+16:base+20], 0) // mod time
			le.PutUint16(dh[base+20:base+22], uint16(pl.flag))
		}
	}
	copy(dh[itemOff:], pool)

	// --- Build header ---
	headerSize := 180
	if s.version == 4 {
		headerSize = 184
	}
	header := make([]byte, headerSize)
	copy(header[0:8], []byte("_ARCHIVE"))
	le.PutUint32(header[8:12], s.version)
	copy(header[12:28], []byte("-BUFFER FOR MD5-"))
	// archive type 128 bytes: keep zeros (or "Repacked")
	// leave zeros
	copy(header[156:172], []byte("-BUFFER FOR MD5-"))
	le.PutUint32(header[172:176], uint32(len(dh)))
	le.PutUint32(header[176:180], uint32(headerSize+len(dh)))
	if s.version == 4 {
		le.PutUint32(header[180:184], 1)
	}

	// --- Compute MD5 of data header (key DF...) ---
	h1 := md5.New()
	h1.Write([]byte("DFC9AF62-FC1B-4180-BC27-11CCE87D3EFF"))
	h1.Write(dh)
	md5Header := h1.Sum(nil)
	copy(header[156:172], md5Header)

	// --- Assemble file data (headers + dh + file payloads with preambles) ---
	// Write to dst via temp file for atomicity
	tmpDst := dst + ".tmp"
	out, err := os.Create(tmpDst)
	if err != nil {
		return err
	}
	// We'll write header+dh first, then file data, then patch second MD5
	if _, err := out.Write(header); err != nil {
		out.Close()
		return err
	}
	if _, err := out.Write(dh); err != nil {
		out.Close()
		return err
	}
	// File data with preambles
	if progress == nil {
		fmt.Println("  Запись данных...")
	}
	for i, pl := range payloads {
		// preamble: 256-byte name padded with zeros
		name := s.files[i].name
		pre := make([]byte, 256)
		copy(pre, name)
		if _, err := out.Write(pre); err != nil {
			out.Close()
			return err
		}
		if s.version == 2 {
			// 4-byte date (0) + 4-byte CRC (of uncompressed)
			var date uint32
			_ = date
			if err := binary.Write(out, le, uint32(0)); err != nil {
				out.Close()
				return err
			}
			crc := crc32.ChecksumIEEE(pl.uncomp)
			if err := binary.Write(out, le, crc); err != nil {
				out.Close()
				return err
			}
		} else {
			crc := crc32.ChecksumIEEE(pl.uncomp)
			if err := binary.Write(out, le, crc); err != nil {
				out.Close()
				return err
			}
		}
		data := pl.uncomp
		if pl.useComp {
			data = pl.comp
		}
		if _, err := out.Write(data); err != nil {
			out.Close()
			return err
		}
		if progress == nil && (i+1)%20 == 0 {
			bar(i+1, total)
		}
	}
	if progress == nil {
		bar(total, total)
	}
	out.Close()

	// --- Second MD5: over everything from headerSize onward (dh + file data) ---
	f, err := os.Open(tmpDst)
	if err != nil {
		return err
	}
	// header is headerSize bytes, dh+data starts at headerSize
	if _, err := f.Seek(int64(headerSize), io.SeekStart); err != nil {
		f.Close()
		return err
	}
	h2 := md5.New()
	h2.Write([]byte("E01519D6-2DB7-4640-AF54-0A23319C56C3"))
	buf := make([]byte, 1<<20)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			h2.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			f.Close()
			return err
		}
	}
	f.Close()
	md5Data := h2.Sum(nil)

	// Patch second MD5 at offset 12
	patch, err := os.OpenFile(tmpDst, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if _, err := patch.Seek(12, io.SeekStart); err != nil {
		patch.Close()
		return err
	}
	if _, err := patch.Write(md5Data); err != nil {
		patch.Close()
		return err
	}
	patch.Close()

	if err := os.Rename(tmpDst, dst); err != nil {
		// fallback copy
		if err2 := copyFile(tmpDst, dst); err2 != nil {
			return err
		}
		os.Remove(tmpDst)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// fileFullPath returns the archive-internal full path for file id.
func (s *SGAFile) fileFullPath(id int) string {
	if id < 0 || id >= len(s.files) {
		return ""
	}
	fe := s.files[id]
	dir := s.dirs[fe.dir]
	if dir.name == "" {
		return fe.name
	}
	return dir.name + "\\" + fe.name
}
