package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	ErrNotFound       = errors.New("registry: not found")
	ErrInvalidName    = errors.New("registry: invalid artifact or version name")
	ErrInvalidFile    = errors.New("registry: invalid file type (allowed: .tar.gz, .tgz, .zip)")
	ErrVersionExists  = errors.New("registry: version already exists")
	ErrEmptyManifest  = errors.New("registry: manifest empty")
)

var (
	artifactNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	versionNameRe  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
)

// Release is one published package version.
type Release struct {
	Version    string    `json:"version"`
	File       string    `json:"file"`
	SHA256     string    `json:"sha256"`
	Size       int64     `json:"size"`
	UploadedAt time.Time `json:"uploaded_at"`
	GitSHA     string    `json:"git_sha,omitempty"`
}

// Manifest lists all releases for an artifact.
type Manifest struct {
	Artifact string    `json:"artifact"`
	Latest   string    `json:"latest"`
	Releases []Release `json:"releases"`
}

// Store manages artifact files on disk.
type Store struct {
	Root string
}

func NewStore(root string) (*Store, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		root = "data/registry"
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	return &Store{Root: abs}, nil
}

func ValidateArtifact(name string) error {
	name = strings.TrimSpace(strings.ToLower(name))
	if !artifactNameRe.MatchString(name) {
		return ErrInvalidName
	}
	return nil
}

func ValidateVersion(version string) error {
	version = strings.TrimSpace(version)
	if !versionNameRe.MatchString(version) {
		return ErrInvalidName
	}
	return nil
}

func ValidateArchiveFilename(name string) error {
	name = strings.TrimSpace(name)
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"):
	case strings.HasSuffix(lower, ".tgz"):
	case strings.HasSuffix(lower, ".zip"):
	default:
		return ErrInvalidFile
	}
	if strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
		return ErrInvalidFile
	}
	return nil
}

func (s *Store) artifactDir(artifact string) (string, error) {
	if err := ValidateArtifact(artifact); err != nil {
		return "", err
	}
	return filepath.Join(s.Root, artifact), nil
}

func (s *Store) manifestPath(artifact string) (string, error) {
	dir, err := s.artifactDir(artifact)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "manifest.json"), nil
}

func (s *Store) ListArtifacts() ([]string, error) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if ValidateArtifact(name) != nil {
			continue
		}
		if _, err := s.LoadManifest(name); err != nil {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func (s *Store) LoadManifest(artifact string) (*Manifest, error) {
	path, err := s.manifestPath(artifact)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m.Artifact == "" {
		m.Artifact = artifact
	}
	return &m, nil
}

func (s *Store) saveManifest(m *Manifest) error {
	dir, err := s.artifactDir(m.Artifact)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "manifest.json")
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) versionDir(artifact, version string) (string, error) {
	if err := ValidateVersion(version); err != nil {
		return "", err
	}
	dir, err := s.artifactDir(artifact)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, version), nil
}

// PutRelease writes the archive and updates manifest. Overwrites file if version exists with same name.
func (s *Store) PutRelease(artifact, version, filename string, r io.Reader, gitSHA string) (*Release, error) {
	artifact = strings.ToLower(strings.TrimSpace(artifact))
	version = strings.TrimSpace(version)
	filename = filepath.Base(strings.TrimSpace(filename))
	if err := ValidateArtifact(artifact); err != nil {
		return nil, err
	}
	if err := ValidateVersion(version); err != nil {
		return nil, err
	}
	if err := ValidateArchiveFilename(filename); err != nil {
		return nil, err
	}

	vdir, err := s.versionDir(artifact, version)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		return nil, err
	}

	dest := filepath.Join(vdir, filename)
	f, err := os.Create(dest)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	written, err := io.Copy(io.MultiWriter(f, h), r)
	closeErr := f.Close()
	if err != nil {
		_ = os.Remove(dest)
		return nil, err
	}
	if closeErr != nil {
		_ = os.Remove(dest)
		return nil, closeErr
	}

	sum := hex.EncodeToString(h.Sum(nil))
	rel := Release{
		Version:    version,
		File:       filename,
		SHA256:     sum,
		Size:       written,
		UploadedAt: time.Now().UTC(),
		GitSHA:     strings.TrimSpace(gitSHA),
	}

	m, err := s.LoadManifest(artifact)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		m = &Manifest{Artifact: artifact, Releases: []Release{}}
	}

	found := false
	for i, existing := range m.Releases {
		if existing.Version == version {
			m.Releases[i] = rel
			found = true
			break
		}
	}
	if !found {
		m.Releases = append(m.Releases, rel)
	}
	if m.Latest == "" {
		m.Latest = version
	}
	sort.Slice(m.Releases, func(i, j int) bool {
		return m.Releases[i].UploadedAt.After(m.Releases[j].UploadedAt)
	})
	if err := s.saveManifest(m); err != nil {
		return nil, err
	}
	return &rel, nil
}

func (s *Store) SetLatest(artifact, version string) error {
	m, err := s.LoadManifest(artifact)
	if err != nil {
		return err
	}
	if !m.hasVersion(version) {
		return ErrNotFound
	}
	m.Latest = version
	return s.saveManifest(m)
}

func (s *Store) DeleteRelease(artifact, version string) error {
	m, err := s.LoadManifest(artifact)
	if err != nil {
		return err
	}
	idx := -1
	for i, rel := range m.Releases {
		if rel.Version == version {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrNotFound
	}
	vdir, err := s.versionDir(artifact, version)
	if err != nil {
		return err
	}
	_ = os.RemoveAll(vdir)

	m.Releases = append(m.Releases[:idx], m.Releases[idx+1:]...)
	if m.Latest == version {
		m.Latest = ""
		if len(m.Releases) > 0 {
			m.Latest = m.Releases[0].Version
		}
	}
	if len(m.Releases) == 0 {
		adir, _ := s.artifactDir(artifact)
		_ = os.RemoveAll(adir)
		return nil
	}
	return s.saveManifest(m)
}

func (m *Manifest) hasVersion(version string) bool {
	for _, r := range m.Releases {
		if r.Version == version {
			return true
		}
	}
	return false
}

func (m *Manifest) GetRelease(version string) (*Release, error) {
	for i := range m.Releases {
		if m.Releases[i].Version == version {
			return &m.Releases[i], nil
		}
	}
	return nil, ErrNotFound
}

func (m *Manifest) LatestRelease() (*Release, error) {
	if m.Latest != "" {
		return m.GetRelease(m.Latest)
	}
	if len(m.Releases) == 0 {
		return nil, ErrNotFound
	}
	return &m.Releases[0], nil
}

// OpenFile returns a reader for a stored archive.
func (s *Store) OpenFile(artifact, version, filename string) (*os.File, error) {
	if err := ValidateArchiveFilename(filename); err != nil {
		return nil, err
	}
	vdir, err := s.versionDir(artifact, version)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(vdir, filepath.Base(filename))
	if !strings.HasPrefix(path, vdir+string(os.PathSeparator)) && path != vdir {
		return nil, ErrInvalidFile
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return f, nil
}

func FormatSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n2 := n / unit; n2 >= unit; n2 /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
