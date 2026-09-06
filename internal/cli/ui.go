package cli

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/marstack-labs/marstack-cloud/internal/version"
)

const (
	consoleReleases  = "https://github.com/MarStack-Labs/marstack-cloud/releases/download"
	consoleDefault   = "/var/lib/marstack/console"
	consoleMaxBytes  = 64 << 20
	consoleSumsFile  = "SHA256SUMS"
	consoleFetchTime = 2 * time.Minute
)

func consoleArchive(release string) string {
	return "marstack_console_" + release + ".tar.gz"
}

func newUICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Install the web console this control plane can serve",
		Long: "Install the web console this control plane can serve.\n\n" +
			"The console is not part of the binary. It is published as its own archive per\n" +
			"release, so a control plane that only answers the API never holds it, and the\n" +
			"console can be replaced without replacing the binary.\n\n" +
			"Once installed, point the control plane at it with\n" +
			"marstack server --ui-dir <directory>.",
	}
	cmd.AddCommand(newUIInstallCmd())
	return cmd
}

func newUIInstallCmd() *cobra.Command {
	var (
		dir     string
		release string
		from    string
	)

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Download and unpack the console",
		Long: "Download the console archive for this binary's version, check it against the\n" +
			"release's SHA256SUMS, and unpack it.\n\n" +
			"--from installs a file already on disk instead of downloading one. The\n" +
			"checksum is not checked in that case, because the archive is whatever you\n" +
			"chose to hand it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if release == "" {
				release = version.Version
			}
			if from == "" && !strings.HasPrefix(release, "v") {
				return fmt.Errorf(
					"this binary reports version %q, which is not a release, so there is no "+
						"console published for it: pass --version vX.Y.Z, or --from a file",
					version.Version)
			}

			archive, err := consoleBytes(cmd.Context(), cmd.ErrOrStderr(), from, release)
			if err != nil {
				return err
			}

			written, err := unpackConsole(bytes.NewReader(archive), dir)
			if err != nil {
				return err
			}
			if !written["index.html"] {
				return fmt.Errorf("that archive holds no index.html, so it is not a console")
			}

			fmt.Fprintf(cmd.OutOrStdout(), "installed %d files to %s\n", len(written), dir)
			fmt.Fprintf(cmd.OutOrStdout(), "serve it with: marstack server --ui-dir %s\n", dir)
			return nil
		},
	}

	cmd.Flags().StringVar(&dir, "dir", consoleDefault, "directory to unpack the console into")
	cmd.Flags().StringVar(&release, "version", "",
		"release to install, such as v0.2.0; defaults to this binary's version")
	cmd.Flags().StringVar(&from, "from", "",
		"install this local .tar.gz instead of downloading one")

	return cmd
}

func consoleBytes(
	ctx context.Context, progress io.Writer, from, release string,
) ([]byte, error) {
	if from != "" {
		return os.ReadFile(from)
	}

	name := consoleArchive(release)
	base := consoleReleases + "/" + release + "/"

	fmt.Fprintf(progress, "fetching %s\n", name)
	archive, err := fetch(ctx, base+name)
	if err != nil {
		return nil, err
	}

	sums, err := fetch(ctx, base+consoleSumsFile)
	if err != nil {
		return nil, fmt.Errorf("the archive was downloaded but %s was not, so nothing "+
			"vouches for it: %w", consoleSumsFile, err)
	}

	if err := checkSum(archive, sums, name); err != nil {
		return nil, err
	}

	fmt.Fprintf(progress, "checksum ok\n")
	return archive, nil
}

func fetch(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, consoleFetchTime)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", url, res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, consoleMaxBytes))
}

func checkSum(archive, sums []byte, name string) error {
	want := ""
	scanner := bufio.NewScanner(bytes.NewReader(sums))
	for scanner.Scan() {
		digest, file, found := strings.Cut(strings.TrimSpace(scanner.Text()), " ")
		if !found {
			continue
		}
		if strings.TrimLeft(file, " *") == name {
			want = digest
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if want == "" {
		return fmt.Errorf("%s names no %s, so there is nothing to check it against",
			consoleSumsFile, name)
	}

	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("%s does not match its checksum: got %s, want %s", name, got, want)
	}
	return nil
}

func unpackConsole(r io.Reader, dir string) (map[string]bool, error) {
	zipped, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("that file is not a gzip archive: %w", err)
	}
	defer zipped.Close()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	written := map[string]bool{}
	budget := int64(consoleMaxBytes)
	archive := tar.NewReader(zipped)

	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}

		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir {
			return nil, fmt.Errorf("%s is not a regular file or a directory, and a console "+
				"archive holds nothing else", header.Name)
		}

		name, err := insideConsole(header.Name)
		if err != nil {
			return nil, err
		}
		if name == "" {
			continue
		}

		target := filepath.Join(dir, filepath.FromSlash(name))
		if header.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return nil, err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, err
		}
		if header.Size > budget {
			return nil, fmt.Errorf("that archive unpacks to more than %d bytes", consoleMaxBytes)
		}

		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return nil, err
		}
		copied, err := io.Copy(file, io.LimitReader(archive, budget))
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return nil, err
		}

		budget -= copied
		written[name] = true
	}

	return written, nil
}

func insideConsole(name string) (string, error) {
	clean := path.Clean(strings.TrimPrefix(strings.ReplaceAll(name, `\`, "/"), "./"))
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%s reaches outside the directory it is unpacked into", name)
	}
	if clean == "." {
		return "", nil
	}

	_, rest, found := strings.Cut(clean, "/")
	if !found {
		return "", nil
	}
	return rest, nil
}
