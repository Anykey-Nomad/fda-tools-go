package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// handleRepackSave handles S inside the archive browser: repack the current
// archive (no file replacements) with a visible warning + progress bar.
// The archive is already closed when this is called; the file on disk is
// replaced atomically only on success.
func handleRepackSave(sgaPath string) error {
	fmt.Println()
	fmt.Println("⚠️  Перепаковка SGA займет некоторое время (сжатие ~сотни MB, пересчет MD5).")
	fmt.Printf("   Исходник: %s\n", filepath.Base(sgaPath))
	fmt.Print("   Продолжить? [Y/n]: ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	if line != "" && line != "y" && line != "yes" && line != "д" && line != "да" {
		fmt.Println("   Отменено.")
		return nil
	}
	tmp := sgaPath + ".repacked.tmp"
	os.Remove(tmp)
	// RebuildSGA already prints its own warning + progress bar when progress==nil.
	if err := RebuildSGA(sgaPath, tmp, nil, nil); err != nil {
		os.Remove(tmp)
		return err
	}
	// Atomic replace: keep original as .bak on first repack if no backup exists
	bak := sgaPath + ".bak"
	if _, err := os.Stat(bak); os.IsNotExist(err) {
		_ = copyFile(sgaPath, bak)
		fmt.Printf("   Оригинал сохранен как %s\n", filepath.Base(bak))
	}
	if err := os.Rename(tmp, sgaPath); err != nil {
		// fallback
		if err2 := copyFile(tmp, sgaPath); err2 != nil {
			os.Remove(tmp)
			return err
		}
		os.Remove(tmp)
	}
	fmt.Printf("✅ Готово: %s перепакован.\n", filepath.Base(sgaPath))
	return nil
}

// handleRepackReplace handles R on a file inside the archive: asks for a
// replacement file on disk, then repacks the archive with that single file
// substituted (still recompressing everything, so the warning/progress bar apply).
func handleRepackReplace(sgaPath string, fileID int, fullPath string) error {
	fmt.Println()
	fmt.Printf("Замена: %s\n", fullPath)
	fmt.Print("Укажите путь к новому файлу (Enter — отмена): ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.TrimSpace(line)
	// allow quoted paths from drag&drop / explorer
	line = strings.Trim(line, "\"'")
	if line == "" {
		fmt.Println("   Отменено.")
		return nil
	}
	// Resolve relative to cwd
	if !filepath.IsAbs(line) {
		cwd, _ := os.Getwd()
		line = filepath.Join(cwd, line)
	}
	data, err := os.ReadFile(line)
	if err != nil {
		return fmt.Errorf("не удалось прочитать %s: %w", line, err)
	}
	fmt.Printf("   %s (%d байт) → %s\n", filepath.Base(line), len(data), fullPath)
	fmt.Println("⚠️  Перепаковка займет некоторое время — идет пересжатие всего архива...")

	key := strings.ToLower(fullPath)
	replacements := map[string][]byte{key: data}
	tmp := sgaPath + ".repacked.tmp"
	os.Remove(tmp)
	if err := RebuildSGA(sgaPath, tmp, replacements, nil); err != nil {
		os.Remove(tmp)
		return err
	}
	bak := sgaPath + ".bak"
	if _, err := os.Stat(bak); os.IsNotExist(err) {
		_ = copyFile(sgaPath, bak)
		fmt.Printf("   Оригинал сохранен как %s\n", filepath.Base(bak))
	}
	if err := os.Rename(tmp, sgaPath); err != nil {
		if err2 := copyFile(tmp, sgaPath); err2 != nil {
			os.Remove(tmp)
			return err
		}
		os.Remove(tmp)
	}
	fmt.Printf("✅ Заменен %s (%d байт).\n", fullPath, len(data))
	_ = fileID
	return nil
}
