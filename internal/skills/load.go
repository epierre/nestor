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

// Size limits. A skill with a larger file is refused whole, never truncated, so instructions and
// reference files always reach the model complete.
const (
	MaxSkillFileBytes = 16 * 1024 // SKILL.md, sent on every load_skill
	MaxFileBytes      = 32 * 1024 // any other file, one read_skill_file result
)

func sizeLimit(path string) int64 {
	if path == skillFile {
		return MaxSkillFileBytes
	}
	return MaxFileBytes
}

// HashDir computes the pinned hash of a skill folder: SHA-256 over every regular file, in
// lexical path order, each written as its length-prefixed slash-separated path followed by the
// SHA-256 of its content. File creation order and timestamps do not matter; any change to a
// path or to content does. Symbolic links and other non-regular files are rejected, so a pinned
// skill can never point outside its folder.
func HashDir(fsys fs.FS) (string, error) {
	f, err := hashFS(fsys)
	return f.sum, err
}

// hashFS hashes fsys and enforces the size limits.
func hashFS(fsys fs.FS) (folder, error) {
	return hashFolder(fsys, true)
}

// folder is what hashing a skill folder found.
type folder struct {
	sum     string
	files   map[string][sha256.Size]byte // content hash of each regular file, by slash path
	skillMD []byte                       // the SKILL.md bytes that were hashed
}

// hashFolder hashes fsys as HashDir does, checking size limits when limits is set. It also keeps the SKILL.md content it hashed, so the
// instructions loaded are exactly the bytes checked against the pin, and the hash of every file,
// so a reference file read later can be checked against what was pinned.
func hashFolder(fsys fs.FS, limits bool) (folder, error) {
	h := sha256.New()
	f := folder{files: map[string][sha256.Size]byte{}}
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
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
		if limits {
			info, err := d.Info()
			if err != nil {
				return err
			}
			if limit := sizeLimit(path); info.Size() > limit {
				return fmt.Errorf("%s is %d bytes, over the %d-byte limit", path, info.Size(), limit)
			}
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		if limit := sizeLimit(path); limits && int64(len(data)) > limit {
			return fmt.Errorf("%s is %d bytes, over the %d-byte limit", path, len(data), limit)
		}
		content := sha256.Sum256(data)
		writeEntry(h, path, content)
		f.files[path] = content
		if path == skillFile {
			f.skillMD = data
		}
		return nil
	})
	if err != nil {
		return folder{}, fmt.Errorf("hash skill folder: %w", err)
	}
	f.sum = "sha256:" + hex.EncodeToString(h.Sum(nil))
	return f, nil
}

func writeEntry(h hash.Hash, path string, content [sha256.Size]byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(path)))
	h.Write(n[:])
	h.Write([]byte(path))
	h.Write(content[:])
}

// Loaded is a skill from skills.yaml whose folder matched its pinned hash.
type Loaded struct {
	Entry Entry
	Skill Skill
	// Dir is the skill's folder, and files the content hash of each file in it when it was
	// loaded. read_skill_file serves only these files, and only while they still match.
	Dir   string
	files map[string][sha256.Size]byte
}

// Load reads each skill listed in cfg from root/<name>. A skill whose folder is missing, does
// not match its pinned hash, or whose SKILL.md is invalid or names another skill is skipped and
// logged as an error; the others still load, so one bad edit cannot take the assistant down.
func Load(root string, cfg Config, log *slog.Logger) []Loaded {
	var out []Loaded
	for _, e := range cfg.Skills {
		l, err := loadOne(root, e)
		if err != nil {
			log.Error("skill skipped", "skill", e.Name, "error", err)
			continue
		}
		out = append(out, l)
	}
	return out
}

// Verify loads the skill e from root/<name> again, failing unless its folder still matches the
// pin and its SKILL.md is valid. sandboxd calls it before every run.
func Verify(root string, e Entry) (Loaded, error) {
	return loadOne(root, e)
}

// HasFile reports whether path (slash-separated, relative to the skill folder) is a file that was
// pinned with the skill.
func (l Loaded) HasFile(path string) bool {
	_, ok := l.files[path]
	return ok
}

func loadOne(root string, e Entry) (Loaded, error) {
	f, err := readFolder(root, e.Name)
	if err != nil {
		return Loaded{}, err
	}
	if f.sum != e.Hash {
		return Loaded{}, fmt.Errorf("content does not match pinned hash (now %s)", f.sum)
	}
	s, err := parseFor(e.Name, f.skillMD)
	if err != nil {
		return Loaded{}, err
	}
	return Loaded{Entry: e, Skill: s, Dir: filepath.Join(root, e.Name), files: f.files}, nil
}

// readFolder hashes the skill folder root/<name>, refusing a name that is not a plain skill name
// and a folder that is itself a symbolic link.
func readFolder(root, name string) (folder, error) {
	if !validName(name) {
		return folder{}, fmt.Errorf("invalid skill name %q", name)
	}
	dir := filepath.Join(root, name)
	info, err := os.Lstat(dir)
	if err != nil {
		return folder{}, fmt.Errorf("open skill folder: %w", err)
	}
	if !info.IsDir() {
		return folder{}, fmt.Errorf("%s is not a folder", dir)
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
	f, err := readFolder(root, name)
	if err == nil {
		_, err = parseFor(name, f.skillMD)
	}
	if err != nil {
		return "", fmt.Errorf("skill %q: %w", name, err)
	}
	return f.sum, nil
}
