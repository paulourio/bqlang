// Package editors embeds and packages VS Code, Kiro, and Neovim extensions for bqlang.
package editors

import (
	"archive/zip"
	"bytes"
	"cmp"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

//go:embed assets/vscode
var vscodeAssets embed.FS

//go:embed assets/nvim
var nvimAssets embed.FS

const (
	vscodeAssetRoot = "assets/vscode"
	nvimAssetRoot   = "assets/nvim"

	extensionPublisher = "paulourio"
	extensionName      = "bqlang"
	defaultSemver      = "0.1.0"
	binPathSentinel    = `"__BQPLS_BIN_PATH__"`

	dirPerm  = 0o750
	filePerm = 0o600
)

var vsixEpoch = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

// ErrInvalidExportTarget is returned when an unknown export target is requested.
var ErrInvalidExportTarget = errors.New("invalid export target")

// ErrInvalidExportPath is returned when an export destination path is empty or invalid.
var ErrInvalidExportPath = errors.New("invalid export output path")

// ExportTarget identifies a supported editor bundle export format.
type ExportTarget string

// Supported export targets.
const (
	TargetVSIX      ExportTarget = "vsix"
	TargetVSCodeDir ExportTarget = "vscode-dir"
	TargetNeovim    ExportTarget = "nvim"
)

// Options configures version and binary path stamping for editor bundles.
type Options struct {
	Version string
	BinPath string
}

// ParseExportTarget parses a CLI target string into an ExportTarget.
func ParseExportTarget(raw string) (ExportTarget, error) {
	switch ExportTarget(strings.TrimSpace(raw)) {
	case TargetVSIX:
		return TargetVSIX, nil
	case TargetVSCodeDir:
		return TargetVSCodeDir, nil
	case TargetNeovim:
		return TargetNeovim, nil
	default:
		return "", fmt.Errorf("%w: %q (expected vsix, vscode-dir, or nvim)", ErrInvalidExportTarget, raw)
	}
}

// NormalizeSemver converts arbitrary build version strings (such as "dev" or
// "v0.2.3+dirty") into a numeric X.Y.Z semantic version required by VSIX manifests.
func NormalizeSemver(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")
	if s == "" {
		return defaultSemver
	}

	if idx := strings.IndexAny(s, "+-"); idx >= 0 {
		s = s[:idx]
	}

	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return defaultSemver
	}

	var nums [3]uint64
	for i, part := range parts {
		if part == "" {
			return defaultSemver
		}

		v, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return defaultSemver
		}

		nums[i] = v
	}

	return fmt.Sprintf("%d.%d.%d", nums[0], nums[1], nums[2])
}

type archiveEntry struct {
	name string
	data []byte
}

// BuildVSIX constructs a deterministic OPC .vsix archive in memory.
func BuildVSIX(opts Options) ([]byte, error) {
	ver := NormalizeSemver(opts.Version)
	extFiles, err := collectVSCodeFiles(ver, opts.BinPath)
	if err != nil {
		return nil, fmt.Errorf("BuildVSIX collect assets: %w", err)
	}

	entries := make([]archiveEntry, 0, len(extFiles)+2)
	entries = append(entries,
		archiveEntry{name: "[Content_Types].xml", data: []byte(contentTypesXML)},
		archiveEntry{name: "extension.vsixmanifest", data: []byte(vsixManifestXML(ver))},
	)

	for _, f := range extFiles {
		entries = append(entries, archiveEntry{
			name: path.Join("extension", f.name),
			data: f.data,
		})
	}

	slices.SortFunc(entries, func(a, b archiveEntry) int {
		return cmp.Compare(a.name, b.name)
	})

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	for _, entry := range entries {
		hdr := &zip.FileHeader{
			Name:     entry.name,
			Method:   zip.Deflate,
			Modified: vsixEpoch,
		}
		hdr.SetMode(0o644)

		w, err := zw.CreateHeader(hdr)
		if err != nil {
			_ = zw.Close()

			return nil, fmt.Errorf("BuildVSIX create %s: %w", entry.name, err)
		}

		if _, err := w.Write(entry.data); err != nil {
			_ = zw.Close()

			return nil, fmt.Errorf("BuildVSIX write %s: %w", entry.name, err)
		}
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("BuildVSIX close zip: %w", err)
	}

	return buf.Bytes(), nil
}

// Export writes the requested editor bundle to outPath.
func Export(target ExportTarget, outPath string, opts Options) error {
	cleanOut := filepath.Clean(strings.TrimSpace(outPath))
	if strings.TrimSpace(outPath) == "" || cleanOut == "." {
		return ErrInvalidExportPath
	}

	switch target {
	case TargetVSIX:
		data, err := BuildVSIX(opts)
		if err != nil {
			return err
		}

		if err := os.MkdirAll(filepath.Dir(cleanOut), dirPerm); err != nil {
			return fmt.Errorf("Export mkdir: %w", err)
		}

		if err := os.WriteFile(cleanOut, data, filePerm); err != nil {
			return fmt.Errorf("Export write vsix: %w", err)
		}

		return nil

	case TargetVSCodeDir:
		files, err := collectVSCodeFiles(NormalizeSemver(opts.Version), opts.BinPath)
		if err != nil {
			return err
		}

		return writeFileTree(cleanOut, files)

	case TargetNeovim:
		files, err := collectNeovimFiles(opts.BinPath)
		if err != nil {
			return err
		}

		return writeFileTree(cleanOut, files)

	default:
		return fmt.Errorf("%w: %q", ErrInvalidExportTarget, target)
	}
}

// InstallVSCode installs the embedded VS Code extension via the `code` CLI,
// falling back to direct extraction into ~/.vscode/extensions when `code` is not on PATH.
func InstallVSCode(ctx context.Context, opts Options) error {
	return installVSIXEditor(ctx, "code", ".vscode", opts)
}

// InstallKiro installs the embedded extension into Kiro via the `kiro` CLI,
// falling back to direct extraction into ~/.kiro/extensions when `kiro` is not on PATH.
func InstallKiro(ctx context.Context, opts Options) error {
	return installVSIXEditor(ctx, "kiro", ".kiro", opts)
}

// InstallNeovim atomically installs the embedded Neovim plugin into
// $XDG_DATA_HOME/nvim/site/pack/bqlang/start/bqpls.nvim (or ~/.local/share/...).
func InstallNeovim(opts Options) error {
	dataHome, err := resolveXDGDataHome()
	if err != nil {
		return fmt.Errorf("InstallNeovim resolve data dir: %w", err)
	}

	files, err := collectNeovimFiles(opts.BinPath)
	if err != nil {
		return fmt.Errorf("InstallNeovim collect assets: %w", err)
	}

	targetDir := filepath.Join(dataHome, "nvim", "site", "pack", "bqlang", "start", "bqpls.nvim")
	if err := atomicInstallDir(targetDir, files); err != nil {
		return fmt.Errorf("InstallNeovim install: %w", err)
	}

	return nil
}

// NeovimPackDir returns the resolved Neovim start-package destination path.
func NeovimPackDir() (string, error) {
	dataHome, err := resolveXDGDataHome()
	if err != nil {
		return "", err
	}

	return filepath.Join(dataHome, "nvim", "site", "pack", "bqlang", "start", "bqpls.nvim"), nil
}

func installVSIXEditor(ctx context.Context, cliName, dotDir string, opts Options) error {
	cliPath, lookupErr := exec.LookPath(cliName)
	if lookupErr != nil {
		if errors.Is(lookupErr, exec.ErrNotFound) {
			return installHeadlessExtensionDir(dotDir, opts)
		}

		return fmt.Errorf("lookup %s CLI: %w", cliName, lookupErr)
	}

	vsixBytes, err := BuildVSIX(opts)
	if err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp("", "bqlang-*.vsix")
	if err != nil {
		return fmt.Errorf("create temp vsix: %w", err)
	}

	tmpPath := tmpFile.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()

	if _, writeErr := tmpFile.Write(vsixBytes); writeErr != nil {
		_ = tmpFile.Close()

		return fmt.Errorf("write temp vsix: %w", writeErr)
	}

	if closeErr := tmpFile.Close(); closeErr != nil {
		return fmt.Errorf("close temp vsix: %w", closeErr)
	}

	cmd := exec.CommandContext(ctx, cliPath, "--install-extension", tmpPath, "--force")
	cmd.Stdin = nil

	out, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(out))
		if trimmed != "" {
			return fmt.Errorf("%s --install-extension failed: %w: %s", cliName, err, trimmed)
		}

		return fmt.Errorf("%s --install-extension failed: %w", cliName, err)
	}

	return nil
}

func installHeadlessExtensionDir(dotDir string, opts Options) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}

	ver := NormalizeSemver(opts.Version)
	files, err := collectVSCodeFiles(ver, opts.BinPath)
	if err != nil {
		return err
	}

	extFolder := fmt.Sprintf("%s.%s-%s", extensionPublisher, extensionName, ver)
	extensionsRoot := filepath.Join(homeDir, dotDir, "extensions")
	targetDir := filepath.Join(extensionsRoot, extFolder)

	if err := atomicInstallDir(targetDir, files); err != nil {
		return err
	}

	return upsertExtensionsJSON(extensionsRoot, extFolder, ver)
}

func upsertExtensionsJSON(extensionsRoot, extFolder, ver string) error {
	manifestPath := filepath.Join(extensionsRoot, "extensions.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		return fmt.Errorf("read %s: %w", manifestPath, err)
	}

	var entries []map[string]any
	if unmarshalErr := json.Unmarshal(data, &entries); unmarshalErr != nil {
		return fmt.Errorf("parse %s: %w", manifestPath, unmarshalErr)
	}

	extID := extensionPublisher + "." + extensionName
	fullPath := filepath.Join(extensionsRoot, extFolder)
	newEntry := map[string]any{
		"identifier": map[string]any{"id": extID},
		"version":    ver,
		"location": map[string]any{
			"$mid":     1,
			"fsPath":   fullPath,
			"external": "file://" + filepath.ToSlash(fullPath),
			"path":     filepath.ToSlash(fullPath),
			"scheme":   "file",
		},
		"relativeLocation": extFolder,
		"metadata": map[string]any{
			"installedTimestamp": time.Now().UnixMilli(),
			"pinned":             true,
			"source":             "vsix",
		},
	}

	replaced := false
	for i, entry := range entries {
		if idMap, ok := entry["identifier"].(map[string]any); ok {
			if idStr, ok := idMap["id"].(string); ok && strings.EqualFold(idStr, extID) {
				entries[i] = newEntry
				replaced = true

				break
			}
		}
	}

	if !replaced {
		entries = append(entries, newEntry)
	}

	encoded, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", manifestPath, err)
	}

	if err := os.WriteFile(manifestPath, encoded, filePerm); err != nil {
		return fmt.Errorf("write %s: %w", manifestPath, err)
	}

	return nil
}

func resolveXDGDataHome() (string, error) {
	if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" {
		return filepath.Clean(xdg), nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home dir: %w", err)
	}

	return filepath.Join(homeDir, ".local", "share"), nil
}

func atomicInstallDir(targetDir string, files []archiveEntry) error {
	parentDir := filepath.Dir(targetDir)
	if err := os.MkdirAll(parentDir, dirPerm); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}

	stageDir, err := os.MkdirTemp(parentDir, ".bqpls-stage-*")
	if err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}

	cleanupStage := true
	defer func() {
		if cleanupStage {
			_ = os.RemoveAll(stageDir)
		}
	}()

	if err := writeFileTree(stageDir, files); err != nil {
		return err
	}

	if err := os.RemoveAll(targetDir); err != nil {
		return fmt.Errorf("remove previous installation: %w", err)
	}

	if err := os.Rename(stageDir, targetDir); err != nil {
		return fmt.Errorf("activate staged installation: %w", err)
	}

	cleanupStage = false

	return nil
}

func writeFileTree(rootDir string, files []archiveEntry) error {
	if err := os.MkdirAll(rootDir, dirPerm); err != nil {
		return fmt.Errorf("create directory %s: %w", rootDir, err)
	}

	cleanRoot := filepath.Clean(rootDir)
	for _, f := range files {
		rel := filepath.FromSlash(f.name)
		dst := filepath.Join(cleanRoot, rel)
		if !isWithinDir(cleanRoot, dst) {
			return fmt.Errorf("unsafe asset path %q", f.name)
		}

		if err := os.MkdirAll(filepath.Dir(dst), dirPerm); err != nil {
			return fmt.Errorf("create parent dir for %s: %w", dst, err)
		}

		if err := os.WriteFile(dst, f.data, filePerm); err != nil {
			return fmt.Errorf("write %s: %w", dst, err)
		}
	}

	return nil
}

func isWithinDir(rootDir, candidate string) bool {
	rel, err := filepath.Rel(rootDir, candidate)
	if err != nil {
		return false
	}

	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func collectVSCodeFiles(ver, binPath string) ([]archiveEntry, error) {
	rawFiles, err := readEmbeddedTree(vscodeAssets, vscodeAssetRoot)
	if err != nil {
		return nil, err
	}

	jsBin, err := quoteJSString(binPath)
	if err != nil {
		return nil, err
	}

	for i, f := range rawFiles {
		switch f.name {
		case "package.json":
			rawFiles[i].data = bytes.Replace(
				f.data,
				[]byte(`"version": "0.1.0"`),
				[]byte(`"version": "`+ver+`"`),
				1,
			)
		case "extension.js":
			rawFiles[i].data = bytes.ReplaceAll(f.data, []byte(binPathSentinel), jsBin)
		}
	}

	return rawFiles, nil
}

func collectNeovimFiles(binPath string) ([]archiveEntry, error) {
	rawFiles, err := readEmbeddedTree(nvimAssets, nvimAssetRoot)
	if err != nil {
		return nil, err
	}

	luaBin := quoteLuaString(binPath)
	for i, f := range rawFiles {
		if f.name == "lua/bqpls/init.lua" {
			rawFiles[i].data = bytes.ReplaceAll(f.data, []byte(binPathSentinel), []byte(luaBin))
		}
	}

	return rawFiles, nil
}

func readEmbeddedTree(fsys fs.FS, root string) ([]archiveEntry, error) {
	var entries []archiveEntry

	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() {
			return nil
		}

		data, readErr := fs.ReadFile(fsys, p)
		if readErr != nil {
			return fmt.Errorf("read embedded %s: %w", p, readErr)
		}

		rel := strings.TrimPrefix(p, root+"/")
		entries = append(entries, archiveEntry{
			name: rel,
			data: data,
		})

		return nil
	})
	if err != nil {
		return nil, err
	}

	slices.SortFunc(entries, func(a, b archiveEntry) int {
		return cmp.Compare(a.name, b.name)
	})

	return entries, nil
}

func quoteJSString(s string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return nil, fmt.Errorf("encode JS string: %w", err)
	}

	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func quoteLuaString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')

	for i := range len(s) {
		ch := s[i]
		switch ch {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if ch < 0x20 || ch == 0x7f {
				fmt.Fprintf(&b, "\\%03d", ch)
			} else {
				b.WriteByte(ch)
			}
		}
	}

	b.WriteByte('"')

	return b.String()
}

const contentTypesXML = `<?xml version="1.0" encoding="utf-8"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="json" ContentType="application/json" />
  <Default Extension="js" ContentType="application/javascript" />
  <Default Extension="vsixmanifest" ContentType="text/xml" />
</Types>
`

func vsixManifestXML(ver string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<PackageManifest Version="2.0.0" xmlns="http://schemas.microsoft.com/developer/vsx-schema/2011">
  <Metadata>
    <Identity Language="en-US" Id=%q Version=%q Publisher=%q />
    <DisplayName>BigQuery SQL (bqlang)</DisplayName>
    <Description xml:space="preserve">GoogleSQL, BigQuery, and Jinja2/dbt language server and syntax support.</Description>
    <Properties>
      <Property Id="Microsoft.VisualStudio.Code.Engine" Value="^1.85.0" />
    </Properties>
  </Metadata>
  <Installation>
    <InstallationTarget Id="Microsoft.VisualStudio.Code" Version="[1.85.0,)" />
  </Installation>
  <Dependencies />
  <Assets>
    <Asset Type="Microsoft.VisualStudio.Code.Manifest" Path="extension/package.json" Addressable="true" />
  </Assets>
</PackageManifest>
`, extensionName, ver, extensionPublisher)
}
