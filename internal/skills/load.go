package skills

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// skillFile is the file in a skill folder that holds its front matter and instructions.
const skillFile = "SKILL.md"

// HashDir computes the pinned hash of a skill folder: SHA-256 over every regular file, in
// lexical path order, each written as its length-prefixed slash-separated path followed by the
// SHA-256 of its content. File creation order and timestamps do not matter; any change to a
// path or to content does. Symbolic links and other non-regular files are rejected, so a pinned
// skill can never point outside its folder.
func HashDir(fsys fs.FS) (string, error) {
	sum, _, err := hashFS(fsys)
	return sum, err
}

// hashFS hashes fsys as HashDir does and also returns the SKILL.md content it hashed, so the
// instructions loaded are exactly the bytes that were checked against the pin.
func hashFS(fsys fs.FS) (sum string, skillMD []byte, err error) {
	h := sha256.New()
	err = fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			return fmt.Errorf("%s is a symbolic link", path)
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			return fmt.Errorf("%s is not a regular file", path)
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		writeEntry(h, path, data)
		if path == skillFile {
			skillMD = data
		}
		return nil
	})
	if err != nil {
		return "", nil, fmt.Errorf("hash skill folder: %w", err)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), skillMD, nil
}

func writeEntry(h hash.Hash, path string, data []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(path)))
	h.Write(n[:])
	h.Write([]byte(path))
	content := sha256.Sum256(data)
	h.Write(content[:])
}

// Loaded is a skill from skills.yaml whose folder matched its pinned hash.
type Loaded struct {
	Entry Entry
	Skill Skill
}

// Load reads each skill listed in cfg from root/<name>. A skill whose folder is missing, does
// not match its pinned hash, or whose SKILL.md is invalid or names another skill is skipped and
// logged as an error; the others still load, so one bad edit cannot take the assistant down.
func Load(root string, cfg Config, log *slog.Logger) []Loaded {
	var out []Loaded
	for _, e := range cfg.Skills {
		s, err := loadOne(root, e)
		if err != nil {
			log.Error("skill skipped", "skill", e.Name, "error", err)
			continue
		}
		out = append(out, Loaded{Entry: e, Skill: s})
	}
	return out
}

func loadOne(root string, e Entry) (Skill, error) {
	sum, skillMD, err := readFolder(root, e.Name)
	if err != nil {
		return Skill{}, err
	}
	if sum != e.Hash {
		return Skill{}, fmt.Errorf("content does not match pinned hash (now %s)", sum)
	}
	return parseFor(e.Name, skillMD)
}

// readFolder hashes the skill folder root/<name>, refusing a name that is not a plain skill name
// and a folder that is itself a symbolic link.
func readFolder(root, name string) (sum string, skillMD []byte, err error) {
	if !validName(name) {
		return "", nil, fmt.Errorf("invalid skill name %q", name)
	}
	dir := filepath.Join(root, name)
	info, err := os.Lstat(dir)
	if err != nil {
		return "", nil, fmt.Errorf("open skill folder: %w", err)
	}
	if !info.IsDir() {
		return "", nil, fmt.Errorf("%s is not a folder", dir)
	}
	return hashFS(os.DirFS(dir))
}

// parseFor parses skillMD and checks that it names the skill whose folder it came from.
func parseFor(name string, skillMD []byte) (Skill, error) {
	if skillMD == nil {
		return Skill{}, errors.New("folder has no " + skillFile)
	}
	s, err := ParseSkill(skillMD)
	if err != nil {
		return Skill{}, err
	}
	if s.Name != name {
		return Skill{}, fmt.Errorf("%s names skill %q", skillFile, s.Name)
	}
	return s, nil
}

// Pin returns the hash to pin for the skill folder root/<name> after checking that its SKILL.md
// parses and names that skill, so a broken skill is not pinned.
func Pin(root, name string) (string, error) {
	sum, skillMD, err := readFolder(root, name)
	if err == nil {
		_, err = parseFor(name, skillMD)
	}
	if err != nil {
		return "", fmt.Errorf("skill %q: %w", name, err)
	}
	return sum, nil
}
